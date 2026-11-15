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
	"github.com/touwaeriol/agent-teams/server/internal/github"
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
		return errors.New("usage: atctl admin reset-password EMAIL [PASSWORD] | atctl admin reseal")
	}
	db, err := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "reset-password":
		return resetPassword(ctx, db, args[1:])
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
		fmt.Printf("resealed %d git connections, github app: %v\n", len(secrets), app)
		return err
	})
}
