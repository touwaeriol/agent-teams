package orchapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
)

// maxMoveArchive caps a session-move archive (the daemon's MAX_ARCHIVE_BYTES).
const maxMoveArchive = 128 << 20

// registerMoves adds the node-token routes of session moves (protocol.md §4.11): the old node
// uploads the archive of a session it exported, the new node downloads it when the session opens.
func (a *API) registerMoves(r gin.IRoutes) {
	if a.e == nil {
		r.PUT("/nodes/moves/:id", offline)
		r.GET("/nodes/moves/:id", offline)
		return
	}
	r.PUT("/nodes/moves/:id", a.uploadMove)
	r.GET("/nodes/moves/:id", a.downloadMove)
}

// Upload admission (audit #55 N5): the archive lands in a bytea column, so a request holds it in
// memory until the UPDATE. A node reads one upload body at a time, and at most maxMoveUploads are
// read at once server-wide; the others wait without reading their body (the daemon treats 4xx as
// final, so they queue rather than get refused). That bounds archive memory to about
// maxMoveUploads × maxMoveArchive, and one node cannot take every slot.
const maxMoveUploads = 2

// moveUploads hands out upload slots: one per node, maxMoveUploads in total.
type moveUploads struct {
	mu    sync.Mutex
	nodes map[string]*nodeSlot
	slots chan struct{}
}

type nodeSlot struct {
	ch   chan struct{}
	refs int
}

func newMoveUploads() *moveUploads {
	return &moveUploads{nodes: map[string]*nodeSlot{}, slots: make(chan struct{}, maxMoveUploads)}
}

// acquire waits for the node's slot and a global one; release must be called when ok.
func (u *moveUploads) acquire(ctx context.Context, nodeID string) (release func(), ok bool) {
	u.mu.Lock()
	ns := u.nodes[nodeID]
	if ns == nil {
		ns = &nodeSlot{ch: make(chan struct{}, 1)}
		u.nodes[nodeID] = ns
	}
	ns.refs++
	u.mu.Unlock()
	drop := func() {
		u.mu.Lock()
		if ns.refs--; ns.refs == 0 {
			delete(u.nodes, nodeID)
		}
		u.mu.Unlock()
	}
	select {
	case ns.ch <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, false
	}
	select {
	case u.slots <- struct{}{}:
	case <-ctx.Done():
		<-ns.ch
		drop()
		return nil, false
	}
	return func() { <-u.slots; <-ns.ch; drop() }, true
}

// uploadMove is PUT /nodes/moves/{id}: only the move's source node, while it is exporting; the body
// must match X-Content-SHA256. Ownership and size are checked before any of the body is read.
func (a *API) uploadMove(c *gin.Context) {
	w, r := c.Writer, c.Request
	node, err := a.e.Gateway.Authenticate(r)
	if err != nil {
		http.Error(w, "token rejected", http.StatusUnauthorized)
		return
	}
	if r.ContentLength > maxMoveArchive {
		http.Error(w, "archive too large", http.StatusRequestEntityTooLarge)
		return
	}
	want := r.Header.Get("X-Content-SHA256")
	if len(want) != sha256.Size*2 {
		http.Error(w, "sha256 does not match", http.StatusBadRequest)
		return
	}
	db := a.e.Core.Pool.Gorm(r.Context())
	var n int64
	if err := db.Raw(`SELECT count(*) FROM at_session_moves WHERE id = ? AND from_node_id = ? AND status = 'exporting'`,
		c.Param("id"), node.ID).Scan(&n).Error; err != nil {
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		http.Error(w, "no such move", http.StatusNotFound)
		return
	}
	release, ok := a.moveUploads.acquire(r.Context(), node.ID)
	if !ok {
		return // the client went away while queued
	}
	defer release()

	// Stream the body: hash while reading, never past the cap.
	var buf bytes.Buffer
	if r.ContentLength > 0 {
		buf.Grow(int(r.ContentLength))
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(&buf, h), http.MaxBytesReader(w, r.Body, maxMoveArchive))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "archive too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil || buf.Len() == 0 {
		http.Error(w, "no archive", http.StatusBadRequest)
		return
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if want != digest {
		http.Error(w, "sha256 does not match", http.StatusBadRequest)
		return
	}
	data := buf.Bytes()
	res := db.Exec(`UPDATE at_session_moves SET archive = ?, size = ?, sha256 = ?, updated_at = now()
		WHERE id = ? AND from_node_id = ? AND status = 'exporting'`, data, len(data), digest, c.Param("id"), node.ID)
	if res.Error != nil {
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	if res.RowsAffected == 0 { // the move left 'exporting' while the body was read
		http.Error(w, "no such move", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// downloadMove is GET /nodes/moves/{id}: only the move's target node, while its session imports.
func (a *API) downloadMove(c *gin.Context) {
	w, r := c.Writer, c.Request
	node, err := a.e.Gateway.Authenticate(r)
	if err != nil {
		http.Error(w, "token rejected", http.StatusUnauthorized)
		return
	}
	var data []byte
	err = a.e.Core.Pool.Gorm(r.Context()).Raw(`SELECT archive FROM at_session_moves
		WHERE id = ? AND to_node_id = ? AND status = 'importing' AND archive IS NOT NULL`, c.Param("id"), node.ID).Row().Scan(&data)
	if err != nil || len(data) == 0 {
		http.Error(w, "no such move", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
