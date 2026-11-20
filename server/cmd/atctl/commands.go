package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/migrations"
)

func migrate(ctx context.Context, args []string) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := migrations.Open(url)
	if err != nil {
		return err
	}
	defer db.Close()
	action := "up"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "up":
		applied, err := migrations.Up(ctx, db)
		for _, v := range applied {
			fmt.Printf("applied %05d\n", v)
		}
		if err == nil && len(applied) == 0 {
			fmt.Println("no pending migrations")
		}
		return err
	case "status":
		lines, err := migrations.Status(ctx, db)
		for _, l := range lines {
			fmt.Println(l)
		}
		return err
	case "version":
		v, err := migrations.Version(ctx, db)
		fmt.Println(v)
		return err
	}
	return fmt.Errorf("unknown migrate action %q (up, status, version)", action)
}

func healthcheck(ctx context.Context, _ []string) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3001"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

// openapi prints the document without touching any database.
func openapi(_ context.Context, _ []string) error {
	doc, err := openAPIDocument()
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(doc))
	return err
}

func admin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atctl admin reset-password EMAIL [PASSWORD] | atctl admin set-role EMAIL admin|user | atctl admin reseal")
	}
	db, err := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "reset-password":
		return resetPassword(ctx, db, args[1:])
	case "set-role":
		return setRole(ctx, db, args[1:])
	case "reseal":
		return reseal(ctx, db)
	}
	return fmt.Errorf("unknown admin command %q", args[0])
}

func resetPassword(ctx context.Context, db *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atctl admin reset-password EMAIL [PASSWORD]")
	}
	user, _, err := db.UserCredentials(ctx, auth.NormalizeEmail(args[0]))
	if err != nil {
		return fmt.Errorf("user %s: %w", args[0], err)
	}
	password, temporary := auth.RandomPassword(), true
	if len(args) > 1 {
		password, temporary = args[1], false
		if reason := auth.CheckPassword(password); reason != "" {
			return errors.New(reason)
		}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := db.SetPassword(ctx, user.ID, hash, temporary); err != nil {
		return err
	}
	if err := db.DeleteUserSessions(ctx, user.ID, ""); err != nil {
		return err
	}
	if temporary {
		fmt.Printf("temporary password for %s: %s (must be changed after signing in)\n", user.Email, password)
	} else {
		fmt.Printf("password for %s updated\n", user.Email)
	}
	return nil
}

// setRole assigns admin or user through the Casbin enforcer (the grouping rule in casbin_rule).
func setRole(ctx context.Context, db *store.Store, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: atctl admin set-role EMAIL admin|user")
	}
	user, _, err := db.UserCredentials(ctx, auth.NormalizeEmail(args[0]))
	if err != nil {
		return fmt.Errorf("user %s: %w", args[0], err)
	}
	if err := httpapi.SetUserRole(db, user.ID, args[1]); err != nil {
		return err
	}
	fmt.Printf("%s is now %s (running consoles apply it within seconds)\n", user.Email, args[1])
	return nil
}

func reseal(ctx context.Context, db *store.Store) error {
	cfg, err := config.Load(config.Environ())
	if err != nil {
		return err
	}
	box := auth.NewSecretBox(cfg.SecretKeys...)
	return db.Tx(ctx, func(tx *store.Store) error {
		secrets, err := tx.SealedSecrets(ctx)
		if err != nil {
			return err
		}
		for id, sealed := range secrets {
			fresh, err := box.Reseal(sealed)
			if err != nil {
				return fmt.Errorf("connection %s: %w", id, err)
			}
			if err := tx.SetConnectionSecret(ctx, id, fresh); err != nil {
				return err
			}
		}
		app, err := github.NewApps(tx, box).Reseal(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("resealed %d git connections, github app: %v\n", len(secrets), app)
		vault, err := credvault.New(cfg.SecretKeys...)
		if err != nil {
			return err
		}
		servers, keys, err := resealRemote(ctx, tx, vault)
		fmt.Printf("rewrapped %d remote server credentials, %d ssh keys\n", servers, keys)
		if err != nil {
			return err
		}
		creds, hostKeys, err := resealGit(ctx, tx, vault)
		fmt.Printf("rewrapped %d git credentials, %d git host key MACs\n", creds, hostKeys)
		return err
	})
}

// resealGit re-wraps standalone Git repository credentials and recomputes their repositories'
// host key MACs made with an older key (git-repos.md §4.1).
func resealGit(ctx context.Context, tx *store.Store, vault *credvault.Vault) (creds, hostKeys int, err error) {
	if !vault.Ready() {
		return 0, 0, nil
	}
	list, err := tx.SealedGitCredentials(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, c := range list {
		sealed, changed, err := vault.Rewrap(credvault.GitCredAAD(c.UserID, c.ID, c.Kind, c.Host, c.Username), c.Sealed)
		if err != nil {
			return creds, hostKeys, fmt.Errorf("git credential %s (%s): %w", c.ID, c.Name, err)
		}
		if !changed {
			continue
		}
		if err := tx.ResealGitCredential(ctx, c.ID, sealed); err != nil {
			return creds, hostKeys, err
		}
		creds++
	}
	repos, err := tx.AllGitReposWithHostKeys(ctx)
	if err != nil {
		return creds, hostKeys, err
	}
	for _, r := range repos {
		data := credvault.GitHostKeyMACData(r.ID, r.Host, r.HostKey)
		ok, current := vault.VerifyMAC(data, r.HostKeyMAC)
		if !ok {
			return creds, hostKeys, fmt.Errorf("git repo %s (%s): host key MAC does not verify", r.ID, r.Slug)
		}
		if current {
			continue
		}
		mac, err := vault.MAC(data)
		if err != nil {
			return creds, hostKeys, err
		}
		if err := tx.ResealGitRepoHostKey(ctx, r.ID, mac); err != nil {
			return creds, hostKeys, err
		}
		hostKeys++
	}
	return creds, hostKeys, nil
}

// resealRemote re-wraps the DEKs of remote server credentials and of the SSH keys in at_settings
// with the first key (envelope encryption: the ciphertexts stay), and recomputes host key MACs that
// were made with an older key. Nothing is written for values already under the first key.
func resealRemote(ctx context.Context, tx *store.Store, vault *credvault.Vault) (servers, keys int, err error) {
	if !vault.Ready() {
		return 0, 0, nil
	}
	list, err := tx.SealedRemoteServers(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, srv := range list {
		aad := credvault.ServerAAD(srv.UserID, srv.ID, srv.AuthKind, srv.Host, srv.Port, srv.Username)
		sealed, changed, err := vault.Rewrap(aad, srv.Sealed)
		if err != nil {
			return servers, keys, fmt.Errorf("remote server %s (%s): %w", srv.ID, srv.Name, err)
		}
		mac := srv.HostKeyMAC
		if srv.HostKey != "" {
			data := credvault.HostKeyMACData(srv.ID, srv.Host, srv.Port, srv.HostKey)
			ok, current := vault.VerifyMAC(data, mac)
			if !ok {
				return servers, keys, fmt.Errorf("remote server %s (%s): host key MAC does not verify", srv.ID, srv.Name)
			}
			if !current {
				if mac, err = vault.MAC(data); err != nil {
					return servers, keys, err
				}
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := tx.ResealRemoteServer(ctx, srv.ID, sealed, mac); err != nil {
			return servers, keys, err
		}
		servers++
	}
	for _, name := range []string{credvault.SessionCAKey, credvault.GatewayHostKeyKey} {
		changed, err := credvault.ResealKey(ctx, tx, vault, name)
		if err != nil {
			return servers, keys, err
		}
		if changed {
			keys++
		}
	}
	return servers, keys, nil
}
