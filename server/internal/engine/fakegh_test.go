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

// defaultRepo is the repository the single-repository helpers (ref, setRef) talk about.
const defaultRepo = "o/r"

// fakePR is a pull request of the fake GitHub.
type fakePR struct {
	number     int
	head, base string
	merged     bool
}

// fakeGitHub keeps refs, merges, check runs and pull requests of several repositories in memory.
// Keys are "owner/name:branch".
type fakeGitHub struct {
	mu        sync.Mutex
	refs      map[string]string
	conflicts map[string]bool // "repo:head" whose merge conflicts
	checks    map[string][]map[string]any
	merges    []string // "repo:head→base"
	pulls     map[string]*fakePR
	prs       int
	n         int
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{refs: map[string]string{"o/r:main": "sha-main", "o/web:main": "sha-web-main", "o/web:dev": "sha-web-dev"},
		conflicts: map[string]bool{}, checks: map[string][]map[string]any{}, pulls: map[string]*fakePR{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", f.user)
	mux.HandleFunc("GET /repos/{o}/{r}", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"full_name": repoOf(r), "default_branch": "main"})
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

func repoOf(r *http.Request) string { return r.PathValue("o") + "/" + r.PathValue("r") }

func key(repo, branch string) string { return repo + ":" + branch }

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

// ref reads a branch of the default repository.
func (f *fakeGitHub) ref(name string) (string, bool) { return f.refIn(defaultRepo, name) }

func (f *fakeGitHub) refIn(repo, name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.refs[key(repo, name)]
	return sha, ok
}

// setRef sets a branch of the default repository.
func (f *fakeGitHub) setRef(name, sha string) { f.setRefIn(defaultRepo, name, sha) }

func (f *fakeGitHub) setRefIn(repo, name, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[key(repo, name)] = sha
}

func (f *fakeGitHub) setConflict(repo, head string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.conflicts[key(repo, head)] = true
	} else {
		delete(f.conflicts, key(repo, head))
	}
}

func (f *fakeGitHub) getRef(w http.ResponseWriter, r *http.Request) {
	sha, ok := f.refIn(repoOf(r), r.PathValue("ref"))
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
	if _, ok := f.refIn(repoOf(r), name); ok {
		write(w, 422, map[string]string{"message": "Reference already exists"})
		return
	}
	f.setRefIn(repoOf(r), name, body.SHA)
	write(w, 201, map[string]any{"ref": body.Ref})
}

func (f *fakeGitHub) updateRef(w http.ResponseWriter, r *http.Request) {
	var body struct{ SHA string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.setRefIn(repoOf(r), r.PathValue("ref"), body.SHA)
	write(w, 200, map[string]any{})
}

func (f *fakeGitHub) deleteRef(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delete(f.refs, key(repoOf(r), r.PathValue("ref")))
	f.mu.Unlock()
	w.WriteHeader(204)
}

func (f *fakeGitHub) merge(w http.ResponseWriter, r *http.Request) {
	var body struct{ Base, Head string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	repo := repoOf(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflicts[key(repo, body.Head)] {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"message":"Merge conflict"}`))
		return
	}
	f.n++
	sha := fmt.Sprintf("sha-merge-%d", f.n)
	f.refs[key(repo, body.Base)] = sha
	f.merges = append(f.merges, repo+":"+body.Head+"→"+body.Base)
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

// createPR numbers pull requests from 7 across repositories.
func (f *fakeGitHub) createPR(w http.ResponseWriter, r *http.Request) {
	var body struct{ Head, Base string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	repo := repoOf(r)
	f.mu.Lock()
	f.prs++
	pr := &fakePR{number: 6 + f.prs, head: body.Head, base: body.Base}
	f.pulls[fmt.Sprintf("%s#%d", repo, pr.number)] = pr
	f.mu.Unlock()
	write(w, 201, map[string]any{"number": pr.number, "html_url": prURL(repo, pr.number), "state": "open"})
}

func prURL(repo string, n int) string { return fmt.Sprintf("https://github.com/%s/pull/%d", repo, n) }

func (f *fakeGitHub) pull(r *http.Request) (*fakePR, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, ok := f.pulls[repoOf(r)+"#"+r.PathValue("n")]
	return pr, ok
}

func (f *fakeGitHub) getPR(w http.ResponseWriter, r *http.Request) {
	pr, ok := f.pull(r)
	if !ok {
		write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	head, _ := f.refIn(repoOf(r), pr.head)
	f.mu.Lock()
	merged := pr.merged
	f.mu.Unlock()
	write(w, 200, map[string]any{"number": pr.number, "html_url": prURL(repoOf(r), pr.number), "state": "open", "merged": merged,
		"mergeable": true, "head": map[string]string{"sha": head, "ref": pr.head}})
}

func (f *fakeGitHub) mergePR(w http.ResponseWriter, r *http.Request) {
	pr, ok := f.pull(r)
	if !ok {
		write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	f.mu.Lock()
	pr.merged = true
	f.mu.Unlock()
	write(w, 200, map[string]any{"merged": true})
}

// prOf returns a repository's pull request (base branch, merged) by head branch.
func (f *fakeGitHub) prOf(repo, head string) (fakePR, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, pr := range f.pulls {
		if strings.HasPrefix(k, repo+"#") && pr.head == head {
			return *pr, true
		}
	}
	return fakePR{}, false
}

func (f *fakeGitHub) mergeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.merges...)
}
