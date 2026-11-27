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

// The console runs one `juicefs gateway --multi-buckets` over the volume as a child process: an
// S3 API in which each top-level folder is a bucket. Nodes reach it through the reverse proxy
// (Config.GatewayURL) with their user's IAM key; the console itself uses the root user, on the
// local address, for buckets, IAM users and files.

// GatewaySpec is how the gateway runs.
type GatewaySpec struct {
	MetaURL      string
	MetaPassword string
	Listen       string // host:port
	RootPassword string // of GatewayRoot
}

// GatewayProcess runs the gateway and starts it again when it exits, until Close.
type GatewayProcess struct {
	// Bin is the juicefs binary.
	Bin string
	// StartTimeout bounds how long Serve waits for the gateway to answer (default 30 s).
	StartTimeout time.Duration
	// RefreshIAM is how often the gateway reloads its IAM users and policies (default 30 s): a
	// user deleted by another replica's gateway stops working within it.
	RefreshIAM time.Duration
	Log        *slog.Logger

	mu      sync.Mutex
	spec    *GatewaySpec
	closed  bool
	quit    chan struct{}
	stopped chan struct{}
	cmd     *exec.Cmd
}

var errGatewayClosed = errors.New("juicefs gateway: shutting down")

// Serve starts the gateway (the first call; later calls must pass the same spec) and waits until
// it answers.
func (g *GatewayProcess) Serve(ctx context.Context, spec GatewaySpec) (Gateway, error) {
	g.mu.Lock()
	switch {
	case g.closed:
		g.mu.Unlock()
		return nil, errGatewayClosed
	case g.spec == nil:
		g.spec, g.quit, g.stopped = &spec, make(chan struct{}), make(chan struct{})
		go g.supervise(spec)
	case *g.spec != spec:
		g.mu.Unlock()
		return nil, errors.New("juicefs gateway: started with other settings")
	}
	g.mu.Unlock()
	endpoint := "http://" + localAddr(spec.Listen)
	if err := waitHealthy(ctx, endpoint, orDefault(g.StartTimeout, 30*time.Second), g.quit); err != nil {
		return nil, fmt.Errorf("juicefs gateway: %w", err)
	}
	return newRootGateway(endpoint, spec.RootPassword), nil
}

// supervise runs the gateway until Close, starting it again (after a growing pause) when it exits.
func (g *GatewayProcess) supervise(spec GatewaySpec) {
	defer close(g.stopped)
	pause := time.Second
	for {
		started := time.Now()
		err := g.run(spec)
		select {
		case <-g.quit:
			return
		default:
		}
		if time.Since(started) > time.Minute {
			pause = time.Second
		}
		g.log().Warn("juicefs gateway exited; starting it again", "err", err, "in", pause)
		select {
		case <-g.quit:
			return
		case <-time.After(pause):
		}
		pause = min(2*pause, 30*time.Second)
	}
}

func (g *GatewayProcess) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// run runs the gateway process once, until it exits or Close stops it.
func (g *GatewayProcess) run(spec GatewaySpec) error {
	cmd := exec.Command(g.Bin, "gateway", "--multi-buckets", "--no-banner", "--log", os.DevNull, "--backup-meta", "0",
		"--refresh-iam-interval", orDefault(g.RefreshIAM, 30*time.Second).String(), "--cache-size", "1024",
		"--no-usage-report", "--metrics", "127.0.0.1:0", spec.MetaURL, spec.Listen)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "META_PASSWORD=" + spec.MetaPassword,
		"MINIO_ROOT_USER=" + GatewayRoot, "MINIO_ROOT_PASSWORD=" + spec.RootPassword}
	setPdeathsig(cmd)
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errGatewayClosed
	}
	if err := cmd.Start(); err != nil {
		g.mu.Unlock()
		return err
	}
	g.cmd = cmd
	g.mu.Unlock()
	err := cmd.Wait()
	if err != nil {
		err = fmt.Errorf("%w: %s", err, lastLines(stderr.String(), 3))
	}
	return err
}

// Close stops the gateway (console shutdown).
func (g *GatewayProcess) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	cmd, stopped := g.cmd, g.stopped
	if g.quit != nil {
		close(g.quit)
	}
	g.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-stopped
		}
	}
}

// localAddr is the address the console dials for listen (a wildcard host is this machine).
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// waitHealthy polls endpoint's liveness until it answers, timeout passes, ctx ends or quit closes.
func waitHealthy(ctx context.Context, endpoint string, timeout time.Duration, quit <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/minio/health/live", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("not answering")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-quit:
			return errGatewayClosed
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// rootGateway is the console's access to the gateway as its root user.
type rootGateway struct {
	endpoint, password string
	s3                 *s3.Client
	iam                MinioIAM
}

func newRootGateway(endpoint, password string) *rootGateway {
	return &rootGateway{endpoint: endpoint, password: password, s3: NewS3Files(endpoint, GatewayRoot, password, "").c,
		iam: MinioIAM{Endpoint: endpoint, AccessKey: GatewayRoot, SecretKey: password}}
}

// Files implements Gateway.
func (g *rootGateway) Files(bucket string) Files {
	return &S3Files{c: g.s3, bucket: bucket}
}

// MakeBucket implements Gateway.
func (g *rootGateway) MakeBucket(ctx context.Context, bucket string) error {
	_, err := g.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return err
}

// RemoveBucket implements Gateway.
func (g *rootGateway) RemoveBucket(ctx context.Context, bucket string) error {
	f := g.Files(bucket)
	entries, err := f.List(ctx, "")
	if noBucket(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := f.Remove(ctx, e.Name); err != nil && !errors.Is(err, ErrNoFile) {
			return err
		}
	}
	_, err = g.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket})
	if noBucket(err) {
		return nil
	}
	return err
}

func noBucket(err error) bool {
	var nb *types.NoSuchBucket
	var api smithy.APIError
	return err != nil && (errors.As(err, &nb) || (errors.As(err, &api) && api.ErrorCode() == "NoSuchBucket"))
}

// Buckets implements Gateway.
func (g *rootGateway) Buckets(ctx context.Context) ([]string, error) {
	out, err := g.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	return names, nil
}

// EnsureUser implements Gateway.
func (g *rootGateway) EnsureUser(ctx context.Context, n UserNames, secretKey string) error {
	return g.iam.ensure(ctx, n.Policy, n.AccessKey, secretKey, BucketPolicy(n.Bucket))
}

// RemoveUser implements Gateway.
func (g *rootGateway) RemoveUser(ctx context.Context, n UserNames) error {
	return g.iam.remove(ctx, n.Policy, n.AccessKey)
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
