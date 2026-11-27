package userfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/minio/madmin-go/v3"
)

// Objects is the platform bucket over S3 with the platform key: the users' folders as prefixes.
type Objects struct {
	c      *s3.Client
	bucket string
}

// NewObjects talks to bucket at endpoint with the given keys (region "" = us-east-1).
func NewObjects(endpoint, region, accessKey, secretKey, bucket string) Objects {
	return Objects{c: NewS3Files(endpoint, region, accessKey, secretKey, bucket, "").c, bucket: bucket}
}

// Files implements Store.
func (o Objects) Files(prefix string) Files {
	return &S3Files{c: o.c, bucket: o.bucket, prefix: prefix}
}

// RemovePrefix implements Store: every object version and unfinished upload under prefix.
func (o Objects) RemovePrefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("refusing to empty the whole bucket")
	}
	p := s3.NewListObjectVersionsPaginator(o.c, &s3.ListObjectVersionsInput{Bucket: &o.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		var ids []types.ObjectIdentifier
		for _, v := range page.Versions {
			ids = append(ids, types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range page.DeleteMarkers {
			ids = append(ids, types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if err := o.deleteAll(ctx, ids); err != nil {
			return err
		}
	}
	ups, err := o.c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: &o.bucket, Prefix: &prefix})
	if err != nil {
		return err
	}
	for _, u := range ups.Uploads {
		_, _ = o.c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &o.bucket, Key: u.Key, UploadId: u.UploadId})
	}
	return nil
}

func (o Objects) deleteAll(ctx context.Context, ids []types.ObjectIdentifier) error {
	if len(ids) == 0 {
		return nil
	}
	out, err := o.c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &o.bucket, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
	if err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		return fmt.Errorf("delete %s: %s", aws.ToString(e.Key), aws.ToString(e.Message))
	}
	return nil
}

// Prefixes implements Store: the folders under users/.
func (o Objects) Prefixes(ctx context.Context) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(o.c, &s3.ListObjectsV2Input{Bucket: &o.bucket, Prefix: aws.String(UsersDir), Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			out = append(out, aws.ToString(cp.Prefix))
		}
	}
	return out, nil
}

// Usage implements Store: the objects under prefix counted (folder markers included).
func (o Objects) Usage(ctx context.Context, prefix string) (Usage, error) {
	var u Usage
	p := s3.NewListObjectsV2Paginator(o.c, &s3.ListObjectsV2Input{Bucket: &o.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return u, err
		}
		for _, obj := range page.Contents {
			u.UsedBytes += aws.ToInt64(obj.Size)
			u.Objects++
		}
	}
	return u, nil
}

// RustFS is Store on a RustFS (or MinIO): objects over S3, each user an IAM user whose policy
// allows its prefix, all with the admin key.
type RustFS struct {
	Objects
	Endpoint  string // https://host[:port]
	AccessKey string
	SecretKey string
}

// NewRustFS is the store of bucket on endpoint with the admin key.
func NewRustFS(endpoint, region, accessKey, secretKey, bucket string) RustFS {
	return RustFS{Objects: NewObjects(endpoint, region, accessKey, secretKey, bucket), Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey}
}

func (r RustFS) iam() MinioIAM {
	return MinioIAM{Endpoint: r.Endpoint, AccessKey: r.AccessKey, SecretKey: r.SecretKey}
}

// Prepare implements Store: the bucket, with a rule dropping uploads left unfinished for a day
// (a node that died mid-upload leaves parts behind).
func (r RustFS) Prepare(ctx context.Context) error {
	_, err := r.c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &r.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if err != nil && !errors.As(err, &owned) && !errors.As(err, &exists) {
		return err
	}
	_, err = r.c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: &r.bucket,
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{
			ID: aws.String("abort-unfinished-uploads"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
			AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(1)},
		}}}})
	if err != nil {
		return fmt.Errorf("lifecycle: %w", err)
	}
	return nil
}

// IssueKey implements Store: the user's IAM user with a new secret and its policy (the same id
// each time, so the previous secret stops working).
func (r RustFS) IssueKey(ctx context.Context, n UserNames, write bool) (Key, error) {
	secret := randomSecret(30)
	if err := r.iam().ensure(ctx, n.Name, n.User, secret, PrefixPolicy(r.bucket, n.Prefix, write)); err != nil {
		return Key{}, err
	}
	return Key{ID: n.User, Secret: secret}, nil
}

// RevokeKey implements Store.
func (r RustFS) RevokeKey(ctx context.Context, n UserNames, keyID string) error {
	return r.iam().remove(ctx, n.Name, orString(keyID, n.User))
}

// Keys implements Store: the IAM users.
func (r RustFS) Keys(ctx context.Context) ([]KeyInfo, error) {
	c, err := r.iam().client()
	if err != nil {
		return nil, err
	}
	users, err := c.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]KeyInfo, 0, len(users))
	for name := range users {
		out = append(out, KeyInfo{ID: name, Name: name})
	}
	return out, nil
}

// MinioIAM is the admin API of RustFS / MinIO.
type MinioIAM struct {
	Endpoint  string // https://host[:port]
	AccessKey string
	SecretKey string
}

func (m MinioIAM) client() (*madmin.AdminClient, error) {
	u, err := url.Parse(m.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad object store endpoint %q", m.Endpoint)
	}
	return madmin.New(u.Host, m.AccessKey, m.SecretKey, u.Scheme == "https")
}

// ensure creates (or resets) user accessKey with policy name attached.
func (m MinioIAM) ensure(ctx context.Context, name, accessKey, secretKey string, policy []byte) error {
	c, err := m.client()
	if err != nil {
		return err
	}
	if err := c.AddCannedPolicy(ctx, name, policy); err != nil {
		return fmt.Errorf("add policy: %w", err)
	}
	if err := c.AddUser(ctx, accessKey, secretKey); err != nil {
		return fmt.Errorf("add user: %w", err)
	}
	if _, err := c.AttachPolicy(ctx, madmin.PolicyAssociationReq{Policies: []string{name}, User: accessKey}); err != nil && !alreadyAttached(err) {
		if err2 := c.SetPolicy(ctx, name, accessKey, false); err2 != nil {
			return fmt.Errorf("attach policy: %w (set policy: %v)", err, err2)
		}
	}
	return nil
}

func alreadyAttached(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already") && strings.Contains(s, "attach")
}

// remove deletes user accessKey and policy name (missing ones are fine).
func (m MinioIAM) remove(ctx context.Context, name, accessKey string) error {
	c, err := m.client()
	if err != nil {
		return err
	}
	if err := c.RemoveUser(ctx, accessKey); err != nil && !notFound(err) {
		return fmt.Errorf("remove user: %w", err)
	}
	if err := c.RemoveCannedPolicy(ctx, name); err != nil && !notFound(err) {
		return fmt.Errorf("remove policy: %w", err)
	}
	return nil
}

func notFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "does not exist") || strings.Contains(s, "nosuch")
}

// S3Files is Files over a bucket: keys are paths, folders are "/"-delimited prefixes and "dir/"
// objects (geesefs writes both).
type S3Files struct {
	c      *s3.Client
	bucket string
	prefix string // the tree's root in the bucket ("" or ending in "/")
}

// NewS3Files is the tree under prefix of bucket at endpoint, with the given keys (region "" =
// us-east-1).
func NewS3Files(endpoint, region, accessKey, secretKey, bucket, prefix string) *S3Files {
	cfg := aws.Config{Region: orString(region, "us-east-1"), Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &S3Files{c: c, bucket: bucket, prefix: prefix}
}

func (f *S3Files) key(name string) *string { return aws.String(f.prefix + name) }

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
	prefix := f.prefix + prefixOf(dir)
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
		if _, err := f.c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &f.bucket, Key: &prefix}); err != nil {
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
	o, err := f.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &f.bucket, Key: f.key(name)})
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
	in := &s3.PutObjectInput{Bucket: &f.bucket, Key: f.key(name), Body: r, ContentLength: aws.Int64(size)}
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
	_, err = f.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &f.bucket, Key: f.key(dir + "/"), Body: strings.NewReader(""), ContentLength: aws.Int64(0)})
	return err
}

// Remove implements Files: a file, or a folder deepest entries first.
func (f *S3Files) Remove(ctx context.Context, name string) error {
	name, err := CleanPath(name)
	if err != nil || name == "" {
		return ErrBadPath
	}
	if _, err := f.c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &f.bucket, Key: f.key(name)}); err == nil {
		_, err = f.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &f.bucket, Key: f.key(name)})
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
	_, err = f.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &f.bucket, Key: f.key(name + "/")})
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
