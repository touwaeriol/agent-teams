package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Config is the AGENT_TEAMS_S3_* settings (config.S3).
type Config struct {
	Endpoint       string // where the control plane calls S3
	PublicEndpoint string // what presigned URLs point at (browsers, node daemons); default Endpoint
	Bucket         string
	AccessKey      string
	SecretKey      string
	Region         string // default us-east-1
	PathStyle      bool
	// HTTPClient overrides the transport (tests); nil = http.DefaultClient semantics of the SDK.
	HTTPClient *http.Client
}

// MaxPutBytes is the largest non-seekable body Put buffers.
const MaxPutBytes = 32 << 20

// S3 is the Store on an S3-compatible service.
type S3 struct {
	bucket  string
	client  *s3.Client // calls from the control plane
	presign *s3.PresignClient
}

var _ Store = (*S3)(nil)

// New builds the S3 store. It does not touch the network (see Check).
func New(c Config) (*S3, error) {
	if c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("blobstore: endpoint, bucket, access key and secret key are required")
	}
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	base := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
		// S3-compatible stores (RustFS, MinIO, the test fake) choke on the CRC32 checksums the SDK
		// adds by default since 2025 (also as query parameters of presigned URLs): only when the
		// operation requires one.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if c.HTTPClient != nil {
		base.HTTPClient = c.HTTPClient
	}
	mk := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(base, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(strings.TrimRight(endpoint, "/"))
			o.UsePathStyle = c.PathStyle
		})
	}
	pub := c.PublicEndpoint
	if pub == "" {
		pub = c.Endpoint
	}
	return &S3{bucket: c.Bucket, client: mk(c.Endpoint), presign: s3.NewPresignClient(mk(pub))}, nil
}

// Check makes sure the bucket is reachable with the credentials (HeadBucket).
func (s *S3) Check(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err
}

// Configured is always true for a built store.
func (s *S3) Configured() bool { return true }

func expires(ttl time.Duration) func(*s3.PresignOptions) {
	return func(o *s3.PresignOptions) { o.Expires = ttl }
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration, o GetOptions) (string, error) {
	in := &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}
	if o.ContentType != "" {
		in.ResponseContentType = aws.String(o.ContentType)
	}
	if o.ContentDisposition != "" {
		in.ResponseContentDisposition = aws.String(o.ContentDisposition)
	}
	r, err := s.presign.PresignGetObject(ctx, in, expires(ttl))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}

func (s *S3) PresignPut(ctx context.Context, key string, ttl time.Duration, size int64, contentType string) (string, map[string]string, error) {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	r, err := s.presign.PresignPutObject(ctx, in, expires(ttl))
	if err != nil {
		return "", nil, err
	}
	return r.URL, signedHeaders(r.SignedHeader), nil
}

// signedHeaders are the headers a client must send with a presigned request (Host is implied).
func signedHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if strings.EqualFold(k, "host") || len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	return out
}

func (s *S3) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: &s.bucket, Key: &key}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := s.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.UploadId), nil
}

func (s *S3) PresignPart(ctx context.Context, key, uploadID string, part int32, ttl time.Duration) (string, error) {
	r, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: &s.bucket, Key: &key, UploadId: &uploadID,
		PartNumber: aws.Int32(part)}, expires(ttl))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}

func (s *S3) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	sorted := append([]Part(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	done := make([]types.CompletedPart, len(sorted))
	for i, p := range sorted {
		done[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &s.bucket, Key: &key,
		UploadId: &uploadID, MultipartUpload: &types.CompletedMultipartUpload{Parts: done}})
	return mapErr(err)
}

func (s *S3) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &s.bucket, Key: &key, UploadId: &uploadID})
	if err = mapErr(err); errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (s *S3) ListMultipart(ctx context.Context, prefix string, olderThan time.Time) ([]PendingUpload, error) {
	var out []PendingUpload
	in := &s3.ListMultipartUploadsInput{Bucket: &s.bucket, Prefix: aws.String(prefix)}
	for {
		page, err := s.client.ListMultipartUploads(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, u := range page.Uploads {
			at := aws.ToTime(u.Initiated)
			if at.Before(olderThan) {
				out = append(out, PendingUpload{Key: aws.ToString(u.Key), UploadID: aws.ToString(u.UploadId), Initiated: at})
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		in.KeyMarker, in.UploadIdMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}

func (s *S3) Head(ctx context.Context, key string) (Object, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return Object{}, mapErr(err)
	}
	return Object{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ETag: strings.Trim(aws.ToString(out.ETag), `"`)}, nil
}

func (s *S3) ReadRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return []byte{}, nil
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key,
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+n-1))})
	if err != nil {
		if isRangeNotSatisfiable(err) {
			return []byte{}, nil
		}
		return nil, mapErr(err)
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, n))
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Body, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	// The SDK signs the payload hash, so it needs a seekable body: server-side writes are small
	// (migration rows ≤ 20 MB, copies of inline text) and are buffered. Big objects never go
	// through the control plane (presigned uploads).
	var body io.Reader = r
	if _, ok := r.(io.ReadSeeker); !ok {
		if size > MaxPutBytes {
			return fmt.Errorf("blobstore: put %s: %d bytes is over the %d of a server-side write", key, size, MaxPutBytes)
		}
		buf, err := io.ReadAll(io.LimitReader(r, size+1))
		if err != nil {
			return err
		}
		if int64(len(buf)) != size {
			return fmt.Errorf("blobstore: put %s: got %d bytes, want %d", key, len(buf), size)
		}
		body = bytes.NewReader(buf)
	}
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body, ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := s.client.PutObject(ctx, in)
	return err
}

func (s *S3) Copy(ctx context.Context, srcKey, dstKey string) error {
	src := s.bucket + "/" + url.PathEscape(srcKey)
	src = strings.ReplaceAll(src, "%2F", "/")
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &s.bucket, Key: &dstKey, CopySource: &src})
	return mapErr(err)
}

func (s *S3) Delete(ctx context.Context, keys ...string) error {
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		objs := make([]types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			objs[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &s.bucket,
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)}})
		if err != nil {
			return err
		}
		for _, e := range out.Errors {
			if aws.ToString(e.Code) != "NoSuchKey" {
				return fmt.Errorf("blobstore: delete %s: %s %s", aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message))
			}
		}
		keys = keys[n:]
	}
	return nil
}

func (s *S3) DeletePrefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("blobstore: refusing to delete an empty prefix")
	}
	in := &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix)}
	for {
		page, err := s.client.ListObjectsV2(ctx, in)
		if err != nil {
			return err
		}
		keys := make([]string, len(page.Contents))
		for i, o := range page.Contents {
			keys[i] = aws.ToString(o.Key)
		}
		if err := s.Delete(ctx, keys...); err != nil {
			return err
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		in.ContinuationToken = page.NextContinuationToken
	}
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var nk *types.NoSuchKey
	var nu *types.NoSuchUpload
	var nf *types.NotFound
	if errors.As(err, &nk) || errors.As(err, &nu) || errors.As(err, &nf) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var api smithy.APIError
	if errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NoSuchUpload") {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}

func isRangeNotSatisfiable(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidRange"
}
