package userfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// B2Capabilities are a user's key capabilities in its prefix: everything a mount needs, and
// B2NoWriteCapabilities those of a folder over its capacity (it can still read and delete).
var (
	B2Capabilities        = []string{"listBuckets", "listFiles", "readFiles", "writeFiles", "deleteFiles"}
	B2NoWriteCapabilities = []string{"listBuckets", "listFiles", "readFiles", "deleteFiles"}
)

// B2API is the Backblaze B2 native API's sign-in address.
const B2API = "https://api.backblazeb2.com"

// B2 is Store on Backblaze B2: objects over its S3 API, each user an application key limited to
// the bucket and its prefix (native API b2_create_key / b2_delete_key). The platform key needs
// writeKeys besides the file capabilities.
type B2 struct {
	Objects
	KeyID  string
	Key    string
	Bucket string
	API    string       // default B2API
	HTTP   *http.Client // default http.DefaultClient

	mu   sync.Mutex
	auth *b2Auth
}

type b2Auth struct {
	accountID string
	token     string
	apiURL    string
	bucketID  string
	at        time.Time
}

// NewB2 is the store of bucket with the platform key keyID / key; endpoint is the bucket's S3
// address (https://s3.<region>.backblazeb2.com).
func NewB2(endpoint, region, keyID, key, bucket string) *B2 {
	return &B2{Objects: NewObjects(endpoint, region, keyID, key), KeyID: keyID, Key: key, Bucket: bucket}
}

// b2AuthTTL is how long a sign-in is used (B2 tokens last 24 h).
const b2AuthTTL = 12 * time.Hour

// b2Error is a native API error answer.
type b2Error struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *b2Error) Error() string { return fmt.Sprintf("b2 %d %s: %s", e.Status, e.Code, e.Message) }

func (b *B2) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	return http.DefaultClient
}

// signIn returns the current sign-in, authorizing again when it is old or fresh is set.
func (b *B2) signIn(ctx context.Context, fresh bool) (*b2Auth, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.auth != nil && !fresh && time.Since(b.auth.at) < b2AuthTTL {
		return b.auth, nil
	}
	a, err := b.authorize(ctx)
	if err != nil {
		return nil, err
	}
	b.auth = a
	return a, nil
}

func (b *B2) authorize(ctx context.Context) (*b2Auth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(orString(b.API, B2API), "/")+"/b2api/v3/b2_authorize_account", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(b.KeyID, b.Key)
	var out struct {
		AccountID string `json:"accountId"`
		Token     string `json:"authorizationToken"`
		APIInfo   struct {
			StorageAPI struct {
				APIURL string `json:"apiUrl"`
			} `json:"storageApi"`
		} `json:"apiInfo"`
	}
	if err := b.do(req, &out); err != nil {
		return nil, fmt.Errorf("b2 sign-in: %w", err)
	}
	a := &b2Auth{accountID: out.AccountID, token: out.Token, apiURL: out.APIInfo.StorageAPI.APIURL, at: time.Now()}
	var buckets struct {
		Buckets []struct {
			ID   string `json:"bucketId"`
			Name string `json:"bucketName"`
		} `json:"buckets"`
	}
	if err := b.post(ctx, a, "b2_list_buckets", map[string]any{"accountId": a.accountID, "bucketName": b.Bucket}, &buckets); err != nil {
		return nil, fmt.Errorf("b2 bucket %s: %w", b.Bucket, err)
	}
	for _, bk := range buckets.Buckets {
		if bk.Name == b.Bucket {
			a.bucketID = bk.ID
		}
	}
	if a.bucketID == "" {
		return nil, fmt.Errorf("b2 bucket %s not found (or the key cannot see it)", b.Bucket)
	}
	return a, nil
}

// call posts a native API call, signing in again once when the token expired.
func (b *B2) call(ctx context.Context, op string, body map[string]any, out any) error {
	a, err := b.signIn(ctx, false)
	if err != nil {
		return err
	}
	err = b.post(ctx, a, op, body, out)
	var e *b2Error
	if !errors.As(err, &e) || e.Status != http.StatusUnauthorized {
		return err
	}
	if a, err = b.signIn(ctx, true); err != nil {
		return err
	}
	return b.post(ctx, a, op, body, out)
}

func (b *B2) post(ctx context.Context, a *b2Auth, op string, body map[string]any, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(a.apiURL, "/")+"/b2api/v3/"+op, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", a.token)
	req.Header.Set("Content-Type", "application/json")
	return b.do(req, out)
}

func (b *B2) do(req *http.Request, out any) error {
	res, err := b.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		e := &b2Error{Status: res.StatusCode}
		_ = json.Unmarshal(raw, e)
		e.Status = res.StatusCode
		return e
	}
	return json.Unmarshal(raw, out)
}

// Folder implements Store: the user's prefix of the platform bucket.
func (b *B2) Folder(n UserNames) Folder { return Folder{Bucket: b.Bucket, Prefix: n.Prefix} }

// HardQuota implements Store: B2 has no quota, the console enforces a soft one.
func (b *B2) HardQuota() bool { return false }

// Prepare implements Store: the bucket must exist (the platform key sees it); its lifecycle
// (keep only the last version, cancel unfinished large files) is set on B2 itself.
func (b *B2) Prepare(ctx context.Context, _ Folder, _ int) error {
	_, err := b.signIn(ctx, false)
	return err
}

// Remove implements Store: every object version under the user's prefix.
func (b *B2) Remove(ctx context.Context, f Folder) error {
	if f.Prefix == "" {
		return errors.New("refusing to empty the whole bucket")
	}
	return b.empty(ctx, f)
}

// Folders implements Store: the prefixes under users/.
func (b *B2) Folders(ctx context.Context) ([]Folder, error) {
	var out []Folder
	p := s3.NewListObjectsV2Paginator(b.c, &s3.ListObjectsV2Input{Bucket: &b.Bucket, Prefix: aws.String(UsersDir), Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			if prefix := aws.ToString(cp.Prefix); prefixID(prefix) != "" {
				out = append(out, Folder{Bucket: b.Bucket, Prefix: prefix})
			}
		}
	}
	return out, nil
}

// IssueKey implements Store: a new application key limited to the bucket and the user's prefix.
func (b *B2) IssueKey(ctx context.Context, n UserNames, f Folder, write bool) (Key, error) {
	a, err := b.signIn(ctx, false)
	if err != nil {
		return Key{}, err
	}
	caps := B2NoWriteCapabilities
	if write {
		caps = B2Capabilities
	}
	var out struct {
		ID  string `json:"applicationKeyId"`
		Key string `json:"applicationKey"`
	}
	err = b.call(ctx, "b2_create_key", map[string]any{"accountId": a.accountID, "capabilities": caps, "keyName": n.Name,
		"bucketId": a.bucketID, "namePrefix": f.Prefix}, &out)
	if err != nil {
		return Key{}, fmt.Errorf("b2 create key: %w", err)
	}
	return Key{ID: out.ID, Secret: out.Key}, nil
}

// RevokeKey implements Store (a key already gone is fine).
func (b *B2) RevokeKey(ctx context.Context, _ UserNames, keyID string) error {
	if keyID == "" {
		return nil
	}
	var out map[string]any
	err := b.call(ctx, "b2_delete_key", map[string]any{"applicationKeyId": keyID}, &out)
	var e *b2Error
	if errors.As(err, &e) && (e.Code == "not_found" || (e.Status == http.StatusBadRequest && keyMissing(e.Message))) {
		return nil
	}
	return err
}

func keyMissing(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not found") || strings.Contains(m, "does not exist") || strings.Contains(m, "no such")
}

// Keys implements Store: the account's application keys.
func (b *B2) Keys(ctx context.Context) ([]KeyInfo, error) {
	a, err := b.signIn(ctx, false)
	if err != nil {
		return nil, err
	}
	var keys []KeyInfo
	start := ""
	for {
		body := map[string]any{"accountId": a.accountID, "maxKeyCount": 1000}
		if start != "" {
			body["startApplicationKeyId"] = start
		}
		var out struct {
			Keys []struct {
				ID   string `json:"applicationKeyId"`
				Name string `json:"keyName"`
			} `json:"keys"`
			Next *string `json:"nextApplicationKeyId"`
		}
		if err := b.call(ctx, "b2_list_keys", body, &out); err != nil {
			return nil, err
		}
		for _, k := range out.Keys {
			keys = append(keys, KeyInfo{ID: k.ID, Name: k.Name})
		}
		if out.Next == nil || *out.Next == "" {
			return keys, nil
		}
		start = *out.Next
	}
}
