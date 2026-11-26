package userfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// The console reaches a volume through `juicefs gateway`, an S3 API over the volume run as a child
// process on 127.0.0.1 (no FUSE, no privilege): started on first use, stopped when idle.

// gatewayBucket is the bucket name the gateway serves the volume under.
const gatewayBucket = "files"

// Gateways runs one juicefs gateway per volume in use.
type Gateways struct {
	// Bin is the juicefs binary.
	Bin string
	// Idle is how long an unused gateway keeps running (default 10 minutes).
	Idle time.Duration
	// StartTimeout bounds a start (default 30 s).
	StartTimeout time.Duration
	Log          *slog.Logger

	mu      sync.Mutex
	running map[string]*gateway
	reaper  sync.Once
}

type gateway struct {
	cmd   *exec.Cmd
	files *S3Files
	last  time.Time
	done  chan struct{}
}

// Files returns the volume's tree, starting its gateway if needed.
func (g *Gateways) Files(ctx context.Context, volume, metaURL, metaPassword string) (Files, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reaper.Do(func() { go g.reap() })
	if gw := g.running[volume]; gw != nil {
		select {
		case <-gw.done:
		default:
			gw.last = time.Now()
			return gw.files, nil
		}
	}
	gw, err := g.start(ctx, metaURL, metaPassword)
	if err != nil {
		return nil, err
	}
	if g.running == nil {
		g.running = map[string]*gateway{}
	}
	g.running[volume] = gw
	return gw.files, nil
}

// Stop stops the volume's gateway (before the volume is destroyed).
func (g *Gateways) Stop(volume string) {
	g.mu.Lock()
	gw := g.running[volume]
	delete(g.running, volume)
	g.mu.Unlock()
	if gw != nil {
		gw.stop()
	}
}

func (g *Gateways) start(ctx context.Context, metaURL, password string) (*gateway, error) {
	addr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	user, secret := "console", randomSecret(24)
	cmd := exec.Command(g.Bin, "gateway", "--no-banner", "--no-bgjob", "--backup-meta", "0", "--log", os.DevNull,
		"--bucket-name", gatewayBucket, "--cache-size", "0", "--no-usage-report", "--metrics", "127.0.0.1:0", metaURL, addr)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "META_PASSWORD=" + password,
		"MINIO_ROOT_USER=" + user, "MINIO_ROOT_PASSWORD=" + secret}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("juicefs gateway: %w", err)
	}
	gw := &gateway{cmd: cmd, last: time.Now(), done: make(chan struct{}), files: NewS3Files("http://"+addr, user, secret, gatewayBucket)}
	go func() { _ = cmd.Wait(); close(gw.done) }()
	if err := g.waitReady(ctx, gw, "http://"+addr); err != nil {
		gw.stop()
		return nil, fmt.Errorf("juicefs gateway: %w %s", err, lastLines(stderr.String(), 3))
	}
	return gw, nil
}

func (g *Gateways) waitReady(ctx context.Context, gw *gateway, base string) error {
	timeout := g.StartTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-gw.done:
			return errors.New("exited")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		resp, err := http.Get(base + "/minio/health/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
	return errors.New("not ready in time")
}

func (g *Gateways) reap() {
	idle := g.Idle
	if idle == 0 {
		idle = 10 * time.Minute
	}
	for range time.Tick(time.Minute) {
		g.mu.Lock()
		var old []*gateway
		for v, gw := range g.running {
			if time.Since(gw.last) > idle {
				old = append(old, gw)
				delete(g.running, v)
			}
		}
		g.mu.Unlock()
		for _, gw := range old {
			gw.stop()
		}
	}
}

func (gw *gateway) stop() {
	_ = gw.cmd.Process.Signal(os.Interrupt)
	select {
	case <-gw.done:
	case <-time.After(5 * time.Second):
		_ = gw.cmd.Process.Kill()
		<-gw.done
	}
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// S3Files is Files over an S3 API serving a volume (the juicefs gateway): keys are paths,
// folders are "/"-delimited prefixes and "dir/" objects.
type S3Files struct {
	c      *s3.Client
	bucket string
}

// NewS3Files talks to endpoint with the given keys.
func NewS3Files(endpoint, accessKey, secretKey, bucket string) *S3Files {
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &S3Files{c: c, bucket: bucket}
}

func prefixOf(dir string) string {
	if dir == "" {
		return ""
	}
	return dir + "/"
}

// List implements Files.
func (f *S3Files) List(ctx context.Context, dir string) ([]Entry, error) {
	dir, err := CleanPath(dir)
	if err != nil {
		return nil, err
	}
	prefix := prefixOf(dir)
	var out []Entry
	p := s3.NewListObjectsV2Paginator(f.c, &s3.ListObjectsV2Input{Bucket: &f.bucket, Prefix: &prefix, Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			if name := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(cp.Prefix), prefix), "/"); name != "" {
				out = append(out, Entry{Name: name, Dir: true})
			}
		}
		for _, o := range page.Contents {
			name := strings.TrimPrefix(aws.ToString(o.Key), prefix)
			if name == "" || strings.HasSuffix(name, "/") {
				continue
			}
			out = append(out, Entry{Name: name, Size: aws.ToInt64(o.Size), ModTime: aws.ToTime(o.LastModified)})
		}
	}
	if len(out) == 0 && dir != "" {
		if _, err := f.c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &f.bucket, Key: aws.String(prefix)}); err != nil {
			return nil, s3NoFile(err)
		}
	}
	sortEntries(out)
	return out, nil
}

// Open implements Files.
func (f *S3Files) Open(ctx context.Context, name string) (io.ReadCloser, Entry, error) {
	name, err := CleanPath(name)
	if err != nil || name == "" {
		return nil, Entry{}, ErrBadPath
	}
	o, err := f.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &f.bucket, Key: &name})
	if err != nil {
		return nil, Entry{}, s3NoFile(err)
	}
	return o.Body, Entry{Name: lastSegment(name), Size: aws.ToInt64(o.ContentLength), ModTime: aws.ToTime(o.LastModified)}, nil
}

// Put implements Files.
func (f *S3Files) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	name, err := CleanPath(name)
	if err != nil || name == "" {
		return ErrBadPath
	}
	_, err = f.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &f.bucket, Key: &name, Body: r, ContentLength: aws.Int64(size)})
	return err
}

// Mkdir implements Files.
func (f *S3Files) Mkdir(ctx context.Context, dir string) error {
	dir, err := CleanPath(dir)
	if err != nil || dir == "" {
		return err
	}
	_, err = f.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &f.bucket, Key: aws.String(dir + "/"), Body: strings.NewReader(""), ContentLength: aws.Int64(0)})
	return err
}

// Remove implements Files: a file, or a folder deepest entries first.
func (f *S3Files) Remove(ctx context.Context, name string) error {
	name, err := CleanPath(name)
	if err != nil || name == "" {
		return ErrBadPath
	}
	if _, err := f.c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &f.bucket, Key: &name}); err == nil {
		_, err = f.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &f.bucket, Key: &name})
		return err
	}
	entries, err := f.List(ctx, name)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := f.Remove(ctx, name+"/"+e.Name); err != nil {
			return err
		}
	}
	_, err = f.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &f.bucket, Key: aws.String(name + "/")})
	return err
}

func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func s3NoFile(err error) error {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	var api smithy.APIError
	if errors.As(err, &nsk) || errors.As(err, &nf) || (errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound")) {
		return ErrNoFile
	}
	return err
}
