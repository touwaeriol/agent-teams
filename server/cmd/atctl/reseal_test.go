package main

import (
	"context"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestResealRemote: after AGENT_TEAMS_SECRET_KEYS=new,old and reseal, remote server credentials,
// host key MACs and the SSH keys open with the new key alone; a second reseal writes nothing.
func TestResealRemote(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	mustVault := func(keys ...string) *credvault.Vault {
		v, err := credvault.New(keys...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	old := mustVault("old")
	u, err := db.CreateUser(ctx, "reseal@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	id := store.NewID("srv")
	aad := credvault.ServerAAD(u.ID, id, credvault.AuthPassword, "h.example", 22, "deploy")
	sealed, _, err := old.SealCredential(aad, credvault.AuthPassword, credvault.Credential{Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRemoteServer(ctx, store.RemoteServer{ID: id, UserID: u.ID, Name: "prod", Host: "h.example", Port: 22,
		Username: "deploy", AuthKind: credvault.AuthPassword}, store.ServerCredential{Sealed: sealed}); err != nil {
		t.Fatal(err)
	}
	hostKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample"
	mac, _ := old.MAC(credvault.HostKeyMACData(id, "h.example", 22, hostKey))
	if _, err := db.SetRemoteServerHostKey(ctx, u.ID, id, hostKey, "SHA256:x", mac); err != nil {
		t.Fatal(err)
	}
	_, ca, err := credvault.EnsureKey(ctx, db, old, credvault.SessionCAKey)
	if err != nil {
		t.Fatal(err)
	}

	both := mustVault("new", "old")
	err = db.Tx(ctx, func(tx *store.Store) error {
		servers, keys, err := resealRemote(ctx, tx, both)
		if servers != 1 || keys != 1 {
			t.Errorf("resealed %d servers, %d keys", servers, keys)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if servers, keys, err := resealRemote(ctx, db, both); err != nil || servers != 0 || keys != 0 {
		t.Fatalf("second reseal %d %d %v", servers, keys, err)
	}

	newOnly := mustVault("new")
	sec, err := db.RemoteServerSecret(ctx, u.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := newOnly.OpenCredential(aad, sec.Sealed); err != nil || c.Password != "pw" {
		t.Fatalf("credential after reseal: %v", err)
	}
	if ok, current := newOnly.VerifyMAC(credvault.HostKeyMACData(id, "h.example", 22, hostKey), sec.HostKeyMAC); !ok || !current {
		t.Fatal("host key MAC after reseal")
	}
	if _, got, err := credvault.EnsureKey(ctx, db, newOnly, credvault.SessionCAKey); err != nil || got.Fingerprint != ca.Fingerprint {
		t.Fatalf("CA after reseal: %v", err)
	}
	// A credential no configured key opens stops the reseal (nothing half-done is hidden).
	if _, _, err := resealRemote(ctx, db, mustVault("unrelated")); err == nil {
		t.Fatal("reseal with an unrelated key succeeded")
	}
}
