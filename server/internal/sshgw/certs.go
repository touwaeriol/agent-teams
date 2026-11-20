package sshgw

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// certInflight bounds the ssh.cert_requests worked on at once (like cred.request: the node link's
// read loop never waits; a dropped request is asked again by the daemon).
const certInflight = 16

var certSlots = make(chan struct{}, certInflight)

const certTimeout = 30 * time.Second

// CertRequest answers ssh.cert_request (protocol.md §8.1) off the node link's read loop. The
// gateway's ownership check (session on this node, not closed, open_gen not older) already ran.
func (g *Gateway) CertRequest(ctx context.Context, node store.Node, r *protocol.SshCertRequest) error {
	req := *r
	select {
	case certSlots <- struct{}{}:
	default:
		g.log.Warn("ssh.cert_request dropped: too many in flight", "node_id", node.ID, "req_id", req.ReqID)
		return nil
	}
	go func() {
		defer func() { <-certSlots }()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), certTimeout)
		defer cancel()
		resp := g.Sign(cctx, node, &req)
		resp.ReqID = req.ReqID
		if err := g.nodes.SendNow(cctx, node.ID, protocol.TypeSshCertResponse, resp); err != nil {
			g.log.Warn("ssh.cert_response not sent", "node_id", node.ID, "req_id", req.ReqID, "err", err)
		}
	}()
	return nil
}

func certRefuse(code string) protocol.SshCertResponse {
	return protocol.SshCertResponse{Ok: false, Error: &code}
}

// Sign checks the request again (the session is this node's, live, at its current open_gen, of a
// role that may use servers) and signs a certificate whose principals are the servers the session
// may use now. Without any server it refuses (no_servers).
func (g *Gateway) Sign(ctx context.Context, node store.Node, r *protocol.SshCertRequest) protocol.SshCertResponse {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(r.PublicKey))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 || pub.Type() != ssh.KeyAlgoED25519 {
		return certRefuse("bad_public_key")
	}
	x, servers, err := g.store.SessionServers(ctx, string(r.SessionID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return certRefuse("session_not_found")
	case err != nil:
		g.log.Warn("ssh cert: session servers", "session_id", r.SessionID, "err", err)
		return certRefuse("unavailable")
	case x.NodeID != node.ID || x.UserID != node.UserID || x.State == "closed":
		return certRefuse("session_not_found")
	case x.OpenGen != int(r.OpenGen):
		return certRefuse("stale_open_gen")
	case !x.MayUseSSH():
		return certRefuse("not_granted")
	case len(servers) == 0:
		return certRefuse("no_servers")
	}
	ca, _, caKey, hostKey, err := g.keys(ctx)
	if err != nil {
		g.log.Warn("ssh cert: keys", "err", err)
		return certRefuse("unavailable")
	}
	names := make([]string, len(servers))
	list := make(protocol.SshServers, len(servers))
	for i, s := range servers {
		names[i] = s.Name
		list[i] = protocol.SshServer{Name: s.Name}
	}
	now := g.now().UTC().Truncate(time.Second)
	row, err := g.store.InsertSSHCert(ctx, store.SSHCert{SessionID: x.SessionID, OpenGen: x.OpenGen, NodeID: node.ID,
		ProjectID: x.ProjectID, ThreadID: x.ThreadID, EmployeeID: x.EmployeeID, Role: x.Role,
		PubkeyFP: ssh.FingerprintSHA256(pub), Principals: names, CAID: caKey.ID,
		ValidAfter: now.Add(-g.cfg.ClockSkew), ValidBefore: now.Add(g.cfg.CertTTL)})
	if err != nil {
		g.log.Warn("ssh cert: record", "session_id", x.SessionID, "err", err)
		return certRefuse("unavailable")
	}
	cert := &ssh.Certificate{
		Key:             pub,
		Serial:          uint64(row.Serial),
		CertType:        ssh.UserCert,
		KeyId:           keyID(x, node.ID),
		ValidPrincipals: names,
		ValidAfter:      uint64(row.ValidAfter.Unix()),
		ValidBefore:     uint64(row.ValidBefore.Unix()),
		Permissions: ssh.Permissions{Extensions: map[string]string{
			"permit-pty": "",
			extSession:   x.SessionID,
			extGen:       strconv.Itoa(x.OpenGen),
			extNode:      node.ID,
		}},
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		g.log.Warn("ssh cert: sign", "err", err)
		return certRefuse("unavailable")
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert)))
	before := row.ValidBefore.UTC()
	return protocol.SshCertResponse{Ok: true, Certificate: &line, ValidBefore: &before,
		KnownHosts: []string{KnownHosts(hostKey)}, Servers: list}
}

// keyID is only for logs (authorization never trusts it, remote-servers.md §4.3).
func keyID(x store.SSHSession, nodeID string) string {
	id := fmt.Sprintf("%s/g%d/%s", x.SessionID, x.OpenGen, x.EmployeeID)
	if x.ThreadID != "" {
		id += "/" + x.ThreadID
	}
	return id + "/" + nodeID
}
