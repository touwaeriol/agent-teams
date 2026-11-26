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
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
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
	closed  bool
	reaper  sync.Once
	quit    chan struct{}
}

// gateway is one volume's child. ready closes when its start ended (err tells how); last and
// refs (under Gateways.mu) keep the reaper off a gateway in use.
type gateway struct {
	cmd   *exec.Cmd
	files *S3Files
	ready chan struct{}
	err   error
	done  chan struct{}
	last  time.Time
	refs  int
	// abort cuts a start short (stop before ready).
	abort     chan struct{}
	abortOnce sync.Once
}

var errGatewaysClosed = errors.New("juicefs gateway: shutting down")

// Files returns the volume's tree, starting its gateway if needed. A start runs outside the lock,
// so other volumes are not held up; callers of the same volume wait for the one start.
func (g *Gateways) Files(ctx context.Context, volume, metaURL, metaPassword string) (Files, error) {
	gw, starting, err := g.entry(volume)
	if err != nil {
		return nil, err
	}
	if starting {
		go g.launch(gw, metaURL, metaPassword)
	}
	select {
	case <-gw.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if gw.err != nil {
		g.forget(volume, gw)
		return nil, gw.err
	}
	return &gatewayFiles{g: g, gw: gw}, nil
}

// entry is the volume's gateway, a new (not yet started) one when none runs.
func (g *Gateways) entry(volume string) (*gateway, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false, errGatewaysClosed
	}
	g.reaper.Do(func() {
		g.quit = make(chan struct{})
		go g.reap(g.quit)
	})
	if gw := g.running[volume]; gw != nil && !gw.exited() {
		gw.last = time.Now()
		return gw, false, nil
	}
	gw := &gateway{ready: make(chan struct{}), done: make(chan struct{}), abort: make(chan struct{}), last: time.Now()}
	if g.running == nil {
		g.running = map[string]*gateway{}
	}
	g.running[volume] = gw
	return gw, true, nil
}

// exited tells a gateway whose start ended and whose process is gone.
func (gw *gateway) exited() bool {
	if !gw.started() {
		return false
	}
	if gw.err != nil {
		return true
	}
	select {
	case <-gw.done:
		return true
	default:
		return false
	}
}

func (g *Gateways) forget(volume string, gw *gateway) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[volume] == gw {
		delete(g.running, volume)
	}
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

// Close stops every gateway and refuses new ones (console shutdown).
func (g *Gateways) Close() {
	g.mu.Lock()
	all := g.running
	g.running, g.closed = nil, true
	if g.quit != nil {
		close(g.quit)
	}
	g.mu.Unlock()
	for _, gw := range all {
		gw.stop()
	}
}

// launch starts gw's process and closes ready.
func (g *Gateways) launch(gw *gateway, metaURL, password string) {
	defer close(gw.ready)
	addr, err := freeAddr()
	if err != nil {
		gw.err = err
		return
	}
	user, secret := "console", randomSecret(24)
	cmd := exec.Command(g.Bin, "gateway", "--no-banner", "--no-bgjob", "--backup-meta", "0", "--log", os.DevNull,
		"--bucket-name", gatewayBucket, "--cache-size", "0", "--no-usage-report", "--metrics", "127.0.0.1:0", metaURL, addr)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "META_PASSWORD=" + password,
		"MINIO_ROOT_USER=" + user, "MINIO_ROOT_PASSWORD=" + secret}
	setPdeathsig(cmd)
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		gw.err = fmt.Errorf("juicefs gateway: %w", err)
		close(gw.done)
		return
	}
	gw.cmd, gw.files = cmd, NewS3Files("http://"+addr, user, secret, gatewayBucket)
	go func() { _ = cmd.Wait(); close(gw.done) }()
	if err := g.waitReady(gw, "http://"+addr); err != nil {
		gw.kill()
		gw.err = fmt.Errorf("juicefs gateway: %w %s", err, lastLines(stderr.String(), 3))
	}
}

func (g *Gateways) waitReady(gw *gateway, base string) error {
	deadline := time.Now().Add(orDefault(g.StartTimeout, 30*time.Second))
	for time.Now().Before(deadline) {
		select {
		case <-gw.done:
			return errors.New("exited")
		case <-gw.abort:
			return errors.New("stopped")
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

func (g *Gateways) reap(quit <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-quit:
			return
		case <-t.C:
			g.reapIdle(orDefault(g.Idle, 10*time.Minute))
		}
	}
}

// reapIdle stops the started gateways unused for idle and not serving a call or a download.
func (g *Gateways) reapIdle(idle time.Duration) {
	g.mu.Lock()
	var old []*gateway
	for v, gw := range g.running {
		if gw.refs == 0 && gw.started() && time.Since(gw.last) > idle {
			old = append(old, gw)
			delete(g.running, v)
		}
	}
	g.mu.Unlock()
	for _, gw := range old {
		gw.stop()
	}
}

func (gw *gateway) started() bool {
	select {
	case <-gw.ready:
		return true
	default:
		return false
	}
}

// acquire marks gw in use; release ends that use.
func (g *Gateways) acquire(gw *gateway) {
	g.mu.Lock()
	gw.refs++
	gw.last = time.Now()
	g.mu.Unlock()
}

func (g *Gateways) release(gw *gateway) {
	g.mu.Lock()
	gw.refs--
	gw.last = time.Now()
	g.mu.Unlock()
}

// stop cuts a start short, waits for it to end, then stops the process.
func (gw *gateway) stop() {
	if gw.abort != nil {
		gw.abortOnce.Do(func() { close(gw.abort) })
	}
	<-gw.ready
	gw.kill()
}

func (gw *gateway) kill() {
	if gw.cmd == nil || gw.cmd.Process == nil {
		return
	}
	_ = gw.cmd.Process.Signal(os.Interrupt)
	select {
	case <-gw.done:
	case <-time.After(5 * time.Second):
		_ = gw.cmd.Process.Kill()
		<-gw.done
	}
}

// gatewayFiles is a running gateway's tree: each call (and each opened file until it is closed)
// holds the gateway in use, so the reaper never stops it mid-transfer.
type gatewayFiles struct {
	g  *Gateways
	gw *gateway
}

func (f *gatewayFiles) use() func() {
	f.g.acquire(f.gw)
	return func() { f.g.release(f.gw) }
}

// List implements Files.
func (f *gatewayFiles) List(ctx context.Context, dir string) ([]Entry, error) {
	defer f.use()()
	return f.gw.files.List(ctx, dir)
}

// Open implements Files: the gateway stays in use until the file is closed.
func (f *gatewayFiles) Open(ctx context.Context, name string) (io.ReadCloser, Entry, error) {
	done := f.use()
	r, e, err := f.gw.files.Open(ctx, name)
	if err != nil {
		done()
		return nil, e, err
	}
	return &releasingReader{ReadCloser: r, done: done}, e, nil
}

// Put implements Files.
func (f *gatewayFiles) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	defer f.use()()
	return f.gw.files.Put(ctx, name, r, size)
}

// Mkdir implements Files.
func (f *gatewayFiles) Mkdir(ctx context.Context, dir string) error {
	defer f.use()()
	return f.gw.files.Mkdir(ctx, dir)
}

// Remove implements Files.
func (f *gatewayFiles) Remove(ctx context.Context, name string) error {
	defer f.use()()
	return f.gw.files.Remove(ctx, name)
}

type releasingReader struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (r *releasingReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.done)
	return err
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
	in := &s3.PutObjectInput{Bucket: &f.bucket, Key: &name, Body: r, ContentLength: aws.Int64(size)}
	_, err = f.c.PutObject(ctx, in, unsignedUnlessSeekable(r))
	return err
}

// unsignedUnlessSeekable sends a body that cannot be rewound with an unsigned payload: over plain
// http the SDK otherwise reads it twice to hash it ("request stream is not seekable"). The length
// is known, so nothing else changes.
func unsignedUnlessSeekable(r io.Reader) func(*s3.Options) {
	return func(o *s3.Options) {
		if _, ok := r.(io.ReadSeeker); ok {
			return
		}
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
	}
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
