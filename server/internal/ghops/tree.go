package ghops

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxTreePaths caps the file list kept per repository and ref.
const MaxTreePaths = 20000

// treeTTL is how long a file list is reused before GitHub is asked again.
const treeTTL = 5 * time.Minute

// Tree is the file list of a repository at a ref.
type Tree struct {
	Ref       string
	Paths     []string
	Truncated bool // GitHub cut the listing short, or MaxTreePaths was reached
}

var trees = struct {
	sync.Mutex
	items map[string]cachedTree
}{items: map[string]cachedTree{}}

type cachedTree struct {
	tree Tree
	at   time.Time
}

// Tree lists the files (blobs) of a repository at ref with the git trees API, cached for a while.
func (g *GitHub) Tree(ctx context.Context, r Repo, ref string) (Tree, error) {
	key := strings.ToLower(r.Slug) + "@" + ref
	trees.Lock()
	c, ok := trees.items[key]
	trees.Unlock()
	if ok && time.Since(c.at) < treeTTL {
		return c.tree, nil
	}
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if _, err := g.call(ctx, r, http.MethodGet, "/git/trees/"+escapeRef(ref)+"?recursive=1", nil, &out); err != nil {
		return Tree{}, err
	}
	t := Tree{Ref: ref, Paths: make([]string, 0, len(out.Tree)), Truncated: out.Truncated}
	for _, e := range out.Tree {
		if e.Type != "blob" {
			continue
		}
		if len(t.Paths) == MaxTreePaths {
			t.Truncated = true
			break
		}
		t.Paths = append(t.Paths, e.Path)
	}
	trees.Lock()
	trees.items[key] = cachedTree{tree: t, at: time.Now()}
	trees.Unlock()
	return t, nil
}

// TextFile reads a text file at ref: ok is false when it does not exist, is not UTF-8 text,
// or is too large for the contents API to return inline.
func (g *GitHub) TextFile(ctx context.Context, r Repo, path, ref string) (text string, ok bool, err error) {
	var out struct {
		Type     string `json:"type"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	_, err = g.call(ctx, r, http.MethodGet, "/contents/"+escapeRef(path)+"?ref="+url.QueryEscape(ref), nil, &out)
	if Status(err) == http.StatusNotFound {
		return "", false, nil
	}
	if err != nil || out.Type != "file" || out.Encoding != "base64" {
		return "", false, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil || !utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0 {
		return "", false, nil
	}
	return string(raw), true, nil
}
