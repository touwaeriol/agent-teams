package engine_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub keeps refs, merges, check runs and one pull request in memory.
type fakeGitHub struct {
	mu        sync.Mutex
	refs      map[string]string
	conflicts map[string]bool // head branches whose merge conflicts
	checks    map[string][]map[string]any
	merges    []string
	prMerged  bool
	prs       int
	n         int
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{refs: map[string]string{"main": "sha-main"}, conflicts: map[string]bool{}, checks: map[string][]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", f.user)
	mux.HandleFunc("GET /repos/{o}/{r}", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"full_name": "o/r", "default_branch": "main"})
	})
	mux.HandleFunc("GET /repos/{o}/{r}/git/ref/heads/{ref...}", f.getRef)
	mux.HandleFunc("POST /repos/{o}/{r}/git/refs", f.createRef)
	mux.HandleFunc("PATCH /repos/{o}/{r}/git/refs/heads/{ref...}", f.updateRef)
	mux.HandleFunc("DELETE /repos/{o}/{r}/git/refs/heads/{ref...}", f.deleteRef)
	mux.HandleFunc("POST /repos/{o}/{r}/merges", f.merge)
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}/check-runs", f.checkRuns)
	mux.HandleFunc("POST /repos/{o}/{r}/pulls", f.createPR)
	mux.HandleFunc("GET /repos/{o}/{r}/pulls/{n}", f.getPR)
	mux.HandleFunc("PUT /repos/{o}/{r}/pulls/{n}/merge", f.mergePR)
	mux.HandleFunc("GET /repos/{o}/{r}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) { write(w, 404, map[string]string{"message": "Not Found"}) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeGitHub) user(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer ghp_good" {
		write(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	write(w, 200, map[string]string{"login": "octo"})
}

func (f *fakeGitHub) ref(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.refs[name]
	return sha, ok
}

func (f *fakeGitHub) setRef(name, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[name] = sha
}

func (f *fakeGitHub) getRef(w http.ResponseWriter, r *http.Request) {
	sha, ok := f.ref(r.PathValue("ref"))
	if !ok {
		write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	write(w, 200, map[string]any{"object": map[string]string{"sha": sha}})
}

func (f *fakeGitHub) createRef(w http.ResponseWriter, r *http.Request) {
	var body struct{ Ref, SHA string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimPrefix(body.Ref, "refs/heads/")
	if _, ok := f.ref(name); ok {
		write(w, 422, map[string]string{"message": "Reference already exists"})
		return
	}
	f.setRef(name, body.SHA)
	write(w, 201, map[string]any{"ref": body.Ref})
}

func (f *fakeGitHub) updateRef(w http.ResponseWriter, r *http.Request) {
	var body struct{ SHA string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.setRef(r.PathValue("ref"), body.SHA)
	write(w, 200, map[string]any{})
}

func (f *fakeGitHub) deleteRef(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delete(f.refs, r.PathValue("ref"))
	f.mu.Unlock()
	w.WriteHeader(204)
}

func (f *fakeGitHub) merge(w http.ResponseWriter, r *http.Request) {
	var body struct{ Base, Head string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflicts[body.Head] {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"message":"Merge conflict"}`))
		return
	}
	f.n++
	sha := fmt.Sprintf("sha-merge-%d", f.n)
	f.refs[body.Base] = sha
	f.merges = append(f.merges, body.Head+"→"+body.Base)
	write(w, 201, map[string]string{"sha": sha})
}

func (f *fakeGitHub) checkRuns(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	runs, ok := f.checks[r.PathValue("sha")]
	f.mu.Unlock()
	if !ok {
		runs = []map[string]any{{"name": "verify", "status": "completed", "conclusion": "success", "html_url": "https://ci/1"}}
	}
	write(w, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
}

func (f *fakeGitHub) createPR(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.prs++
	f.mu.Unlock()
	write(w, 201, map[string]any{"number": 7, "html_url": "https://github.com/o/r/pull/7", "state": "open"})
}

func (f *fakeGitHub) getPR(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	merged := f.prMerged
	f.mu.Unlock()
	head, _ := f.ref("thread/1-login")
	write(w, 200, map[string]any{"number": 7, "html_url": "https://github.com/o/r/pull/7", "state": "open", "merged": merged,
		"mergeable": true, "head": map[string]string{"sha": head, "ref": "thread/1-login"}})
}

func (f *fakeGitHub) mergePR(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.prMerged = true
	f.mu.Unlock()
	write(w, 200, map[string]any{"merged": true})
}

func (f *fakeGitHub) mergeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.merges...)
}
