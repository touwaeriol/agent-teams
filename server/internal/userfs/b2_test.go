package userfs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// fakeB2 is the part of the B2 native API the store uses.
type fakeB2 struct {
	mu       sync.Mutex
	url      string
	tokens   int
	expired  bool // the next call answers 401 expired_auth_token
	keys     map[string]map[string]any
	n        int
	authHits int
}

func (f *fakeB2) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply := func(status int, v any) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		if r.URL.Path == "/b2api/v3/b2_authorize_account" {
			if id, key, ok := r.BasicAuth(); !ok || id != "kid" || key != "ksecret" {
				reply(401, map[string]any{"status": 401, "code": "unauthorized", "message": "bad key"})
				return
			}
			f.authHits++
			f.tokens++
			reply(200, map[string]any{"accountId": "acc", "authorizationToken": fmt.Sprintf("tok%d", f.tokens),
				"apiInfo": map[string]any{"storageApi": map[string]any{"apiUrl": f.url, "s3ApiUrl": "https://s3.us-west-004.backblazeb2.com"}}})
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("tok%d", f.tokens) || f.expired {
			f.expired = false
			reply(401, map[string]any{"status": 401, "code": "expired_auth_token", "message": "expired"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/b2api/v3/b2_list_buckets":
			if body["bucketName"] != "fs" {
				reply(200, map[string]any{"buckets": []any{}})
				return
			}
			reply(200, map[string]any{"buckets": []any{map[string]any{"bucketId": "bid", "bucketName": "fs"}}})
		case "/b2api/v3/b2_create_key":
			f.n++
			id := fmt.Sprintf("004key%d", f.n)
			body["applicationKeyId"] = id
			f.keys[id] = body
			reply(200, map[string]any{"applicationKeyId": id, "applicationKey": "K" + id, "keyName": body["keyName"]})
		case "/b2api/v3/b2_delete_key":
			id, _ := body["applicationKeyId"].(string)
			if _, ok := f.keys[id]; !ok {
				reply(400, map[string]any{"status": 400, "code": "bad_request", "message": "Key not found: " + id})
				return
			}
			delete(f.keys, id)
			reply(200, map[string]any{"applicationKeyId": id})
		case "/b2api/v3/b2_list_keys":
			var ids []string
			for id := range f.keys {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			start, _ := body["startApplicationKeyId"].(string)
			var page []any
			var next any
			for _, id := range ids {
				if id < start {
					continue
				}
				if len(page) == 1 { // one per page, to exercise paging
					next = id
					break
				}
				page = append(page, map[string]any{"applicationKeyId": id, "keyName": f.keys[id]["keyName"]})
			}
			reply(200, map[string]any{"keys": page, "nextApplicationKeyId": next})
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			reply(404, map[string]any{"status": 404, "code": "not_found"})
		}
	})
}

// TestB2Keys: keys limited to the bucket and the user's prefix (fewer capabilities when
// read-only), revoked idempotently, listed across pages; an expired token signs in again.
func TestB2Keys(t *testing.T) {
	f := &fakeB2{keys: map[string]map[string]any{}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	f.url = srv.URL
	ctx := context.Background()
	b := NewB2("https://s3.us-west-004.backblazeb2.com", "us-west-004", "kid", "ksecret", "fs")
	b.API = srv.URL
	if err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	n := NamesOf("u1")
	rw, err := b.IssueKey(ctx, n, true)
	if err != nil || rw.ID == "" || rw.Secret != "K"+rw.ID {
		t.Fatalf("issue: %+v %v", rw, err)
	}
	got := f.keys[rw.ID]
	if got["bucketId"] != "bid" || got["namePrefix"] != n.Prefix || got["keyName"] != n.Name || got["accountId"] != "acc" ||
		!slices.Contains(toStrings(got["capabilities"]), "writeFiles") {
		t.Fatalf("create_key body %+v", got)
	}
	f.expired = true
	ro, err := b.IssueKey(ctx, n, false)
	if err != nil {
		t.Fatalf("issue after expiry: %v", err)
	}
	if caps := toStrings(f.keys[ro.ID]["capabilities"]); slices.Contains(caps, "writeFiles") || !slices.Contains(caps, "deleteFiles") {
		t.Fatalf("read-only capabilities %v", caps)
	}
	if f.authHits != 2 {
		t.Fatalf("signed in %d times", f.authHits)
	}
	keys, err := b.Keys(ctx)
	if err != nil || len(keys) != 2 || keys[0].Name != n.Name {
		t.Fatalf("keys %+v %v", keys, err)
	}
	for range 2 {
		if err := b.RevokeKey(ctx, n, rw.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
	}
	if err := b.RevokeKey(ctx, n, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.keys[rw.ID]; ok || len(f.keys) != 1 {
		t.Fatalf("keys left %v", f.keys)
	}

	wrong := NewB2("https://s3.example", "", "kid", "ksecret", "nope")
	wrong.API = srv.URL
	if err := wrong.Prepare(ctx); err == nil {
		t.Fatal("missing bucket accepted")
	}
	bad := NewB2("https://s3.example", "", "kid", "wrong", "fs")
	bad.API = srv.URL
	if _, err := bad.IssueKey(ctx, n, true); err == nil {
		t.Fatal("bad platform key accepted")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
