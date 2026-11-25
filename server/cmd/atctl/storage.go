package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/touwaeriol/agent-teams/server/internal/blobmigrate"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const storageUsage = `usage: atctl storage migrate|verify|purge-data|restore [--table attachments|shared|all] [--batch N] [--limit N] [--dry-run]

  migrate     copy bytes from the database (data column) to the object store and record the key (data is kept)
  verify      check every migrated row that still has its data: object size and sha256
  purge-data  verify each migrated row again and clear its data column when the object matches
  restore     rollback: copy objects back into data for rows whose data was cleared (<= 20 MB)

Needs DATABASE_URL and AGENT_TEAMS_S3_*. Every step can be re-run and interrupted. Back up the
database before migrate and before purge-data.`

func storageCmd(ctx context.Context, args []string) error {
	return runStorage(ctx, args, os.Getenv, os.Stdout)
}

func runStorage(ctx context.Context, args []string, getenv config.Getenv, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(storageUsage)
	}
	step := args[0]
	fs := flag.NewFlagSet("storage "+step, flag.ContinueOnError)
	fs.SetOutput(out)
	var o blobmigrate.Options
	fs.StringVar(&o.Table, "table", blobmigrate.All, "attachments, shared or all")
	fs.IntVar(&o.Batch, "batch", 50, "rows per query")
	fs.IntVar(&o.Limit, "limit", 0, "stop after this many rows per table (0 = all)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "count only, change nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	o.Out = out
	s3, err := config.LoadS3(getenv)
	if err != nil {
		return err
	}
	if !s3.Configured() {
		return errors.New("set AGENT_TEAMS_S3_ENDPOINT, AGENT_TEAMS_S3_BUCKET, AGENT_TEAMS_S3_ACCESS_KEY and AGENT_TEAMS_S3_SECRET_KEY")
	}
	bs, err := blobstore.New(blobstore.Config{Endpoint: s3.Endpoint, PublicEndpoint: s3.PublicEndpoint, Bucket: s3.Bucket,
		AccessKey: s3.AccessKey, SecretKey: s3.SecretKey, Region: s3.Region, PathStyle: s3.PathStyle})
	if err != nil {
		return err
	}
	if err := bs.Check(ctx); err != nil {
		return fmt.Errorf("object store %s bucket %s: %w", s3.Endpoint, s3.Bucket, err)
	}
	if getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := store.Open(ctx, getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	r := &blobmigrate.Runner{DB: db.Pool, Blobs: bs}
	steps := map[string]func(context.Context, blobmigrate.Options) ([]blobmigrate.Report, error){
		"migrate": r.Migrate, "verify": r.Verify, "purge-data": r.PurgeData, "restore": r.Restore,
	}
	fn, ok := steps[step]
	if !ok {
		return fmt.Errorf("unknown storage step %q\n%s", step, storageUsage)
	}
	reports, err := fn(ctx, o)
	failed := 0
	for _, rep := range reports {
		dry := ""
		if o.DryRun {
			dry = " (dry run)"
		}
		fmt.Fprintf(out, "%s %s%s\n", step, rep, dry)
		for _, e := range rep.Errors {
			fmt.Fprintf(out, "  %s\n", e)
		}
		failed += rep.Failed
	}
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d rows failed (listed above)", failed)
	}
	return nil
}
