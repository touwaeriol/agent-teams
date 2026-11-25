package orchapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
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

// uploadMove is PUT /nodes/moves/{id}: only the move's source node, while it is exporting; the body
// must match X-Content-SHA256.
func (a *API) uploadMove(c *gin.Context) {
	w, r := c.Writer, c.Request
	node, err := a.e.Gateway.Authenticate(r)
	if err != nil {
		nodegw.WriteAuthError(w, err)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMoveArchive))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "archive too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil || len(data) == 0 {
		http.Error(w, "no archive", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if want := r.Header.Get("X-Content-SHA256"); want != digest {
		http.Error(w, "sha256 does not match", http.StatusBadRequest)
		return
	}
	res := a.e.Core.Pool.Gorm(r.Context()).Exec(`UPDATE at_session_moves SET archive = ?, size = ?, sha256 = ?, updated_at = now()
		WHERE id = ? AND from_node_id = ? AND status = 'exporting'`, data, len(data), digest, c.Param("id"), node.ID)
	if res.Error != nil {
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	if res.RowsAffected == 0 {
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
		nodegw.WriteAuthError(w, err)
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
