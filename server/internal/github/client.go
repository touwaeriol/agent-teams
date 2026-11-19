// Package github is the slice of the GitHub REST API the control plane needs:
// token owner, repository and branch listings, the GitHub App flow (manifest
// conversion, user authorization on install, installation tokens) and the
// App's own credentials and token cache.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Default endpoints.
const (
	API = "https://api.github.com"
	Web = "https://github.com"
)

// Error is GitHub refusing a request, or input that is not a GitHub repository.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

var (
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	repoPrefixes = []string{"https://github.com/", "http://github.com/", "git@github.com:", "github.com/"}
)

// ParseRepo returns owner/repo from a GitHub URL, an SSH URL or owner/repo (v1 rules).
func ParseRepo(text string) (string, error) {
	value := strings.TrimSpace(text)
	for _, p := range repoPrefixes {
		value = strings.TrimPrefix(value, p)
	}
	value = strings.SplitN(strings.SplitN(value, "?", 2)[0], "#", 2)[0]
	var parts []string
	for _, p := range strings.Split(value, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	slug := strings.TrimSuffix(strings.Join(parts, "/"), ".git")
	if !repoPattern.MatchString(slug) {
		return "", &Error{Status: 422, Message: fmt.Sprintf(
			"不是 GitHub 仓库地址：%s（支持 https://github.com/owner/repo 或 owner/repo）", text)}
	}
	return slug, nil
}

// Repo is the summary the platform keeps of a repository.
type Repo struct {
	FullName      string  `json:"full_name"`
	Private       bool    `json:"private"`
	DefaultBranch string  `json:"default_branch"`
	Description   string  `json:"description"`
	HTMLURL       string  `json:"html_url"`
	PushedAt      *string `json:"pushed_at"`
}

// Account is the owner of an installation.
type Account struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User | Organization
}

// Installation is a GitHub App installation.
type Installation struct {
	ID          int64             `json:"id"`
	Account     Account           `json:"account"`
	Permissions map[string]string `json:"permissions"`
	HTMLURL     string            `json:"html_url"`
}

// Token is an installation access token.
type Token struct {
	Value     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TokenRequest narrows an installation token (architecture.md §10.4).
type TokenRequest struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

// Client calls the GitHub REST API.
type Client struct {
	BaseURL string
	WebURL  string
	HTTP    *http.Client
}

// NewClient returns a client for github.com.
func NewClient() *Client {
	return &Client{BaseURL: API, WebURL: Web, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type call struct {
	method, base, path, token string
	query                     url.Values
	body                      any
}

func (c *Client) do(ctx context.Context, cl call, out any) (http.Header, error) {
	req, err := c.request(ctx, cl)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &Error{Status: 503, Message: fmt.Sprintf("连不上 GitHub：%v", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err := statusError(resp.StatusCode, raw); err != nil {
		return nil, err
	}
	if out == nil {
		return resp.Header, nil
	}
	return resp.Header, json.Unmarshal(raw, out)
}

func (c *Client) request(ctx context.Context, cl call) (*http.Request, error) {
	guardTx(cl.method, cl.path)
	base := cl.base
	if base == "" {
		base = c.BaseURL
	}
	target := base + cl.path
	if len(cl.query) > 0 {
		target += "?" + cl.query.Encode()
	}
	var body io.Reader
	if cl.body != nil {
		raw, err := json.Marshal(cl.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agent-teams")
	if cl.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cl.token != "" {
		req.Header.Set("Authorization", "Bearer "+cl.token)
	}
	return req, nil
}

func statusError(code int, body []byte) error {
	switch {
	case code == 401 || code == 403:
		return &Error{Status: code, Message: "GitHub 拒绝了这个令牌（无效、过期或权限不够）"}
	case code == 404:
		return &Error{Status: code, Message: "找不到这个仓库，或者令牌没有它的访问权限"}
	case code >= 400:
		text := string(body)
		if len(text) > 200 {
			text = text[:200]
		}
		return &Error{Status: code, Message: fmt.Sprintf("GitHub 返回 %d：%s", code, text)}
	}
	return nil
}

func hasNext(h http.Header) bool { return strings.Contains(h.Get("Link"), `rel="next"`) }

func page(n int) url.Values {
	return url.Values{"per_page": {"100"}, "page": {strconv.Itoa(n)}}
}

// Login returns the account a token belongs to.
func (c *Client) Login(ctx context.Context, token string) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	_, err := c.do(ctx, call{method: "GET", path: "/user", token: token}, &out)
	return out.Login, err
}

// UserRepos lists one page of repositories a user token can see.
func (c *Client) UserRepos(ctx context.Context, token string, n int) ([]Repo, bool, error) {
	q := page(n)
	q.Set("sort", "pushed")
	q.Set("affiliation", "owner,collaborator,organization_member")
	var out []Repo
	h, err := c.do(ctx, call{method: "GET", path: "/user/repos", token: token, query: q}, &out)
	return out, err == nil && hasNext(h), err
}

// InstallationRepos lists one page of repositories an installation token can see.
func (c *Client) InstallationRepos(ctx context.Context, token string, n int) ([]Repo, bool, error) {
	var out struct {
		Repositories []Repo `json:"repositories"`
	}
	h, err := c.do(ctx, call{method: "GET", path: "/installation/repositories", token: token, query: page(n)}, &out)
	return out.Repositories, err == nil && hasNext(h), err
}

// Repo fetches one repository.
func (c *Client) Repo(ctx context.Context, token, slug string) (Repo, error) {
	var out Repo
	_, err := c.do(ctx, call{method: "GET", path: "/repos/" + slug, token: token}, &out)
	return out, err
}

// Branches lists one page of branch names.
func (c *Client) Branches(ctx context.Context, token, slug string, n int) ([]string, bool, error) {
	var out []struct {
		Name string `json:"name"`
	}
	h, err := c.do(ctx, call{method: "GET", path: "/repos/" + slug + "/branches", token: token, query: page(n)}, &out)
	names := make([]string, len(out))
	for i, b := range out {
		names[i] = b.Name
	}
	return names, err == nil && hasNext(h), err
}

// ConvertManifest exchanges the manifest code for the new App's credentials.
func (c *Client) ConvertManifest(ctx context.Context, code string) (map[string]any, error) {
	var out map[string]any
	_, err := c.do(ctx, call{method: "POST", path: "/app-manifests/" + url.PathEscape(code) + "/conversions"}, &out)
	return out, err
}

// InstallationToken mints an installation token with an App JWT.
func (c *Client) InstallationToken(ctx context.Context, jwt string, installationID int64, req TokenRequest) (Token, error) {
	var out Token
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	var body any = req
	if len(req.Repositories) == 0 && len(req.Permissions) == 0 {
		body = nil
	}
	_, err := c.do(ctx, call{method: "POST", path: path, token: jwt, body: body}, &out)
	return out, err
}

// Installation reads an installation (account, granted permissions) with an App JWT.
func (c *Client) Installation(ctx context.Context, jwt string, installationID int64) (Installation, error) {
	var out Installation
	_, err := c.do(ctx, call{method: "GET", path: fmt.Sprintf("/app/installations/%d", installationID), token: jwt}, &out)
	return out, err
}

// UserToken exchanges an OAuth code for a user token (installation ownership check).
func (c *Client) UserToken(ctx context.Context, clientID, clientSecret, code string) (string, error) {
	var out map[string]any
	body := map[string]string{"client_id": clientID, "client_secret": clientSecret, "code": code}
	if _, err := c.do(ctx, call{method: "POST", base: c.WebURL, path: "/login/oauth/access_token", body: body}, &out); err != nil {
		return "", err
	}
	token, _ := out["access_token"].(string)
	if token == "" {
		desc, _ := out["error_description"].(string)
		return "", &Error{Status: 422, Message: "GitHub 授权失败：" + desc}
	}
	return token, nil
}

// UserInstallations lists the installations a user token can see.
func (c *Client) UserInstallations(ctx context.Context, userToken string) ([]Installation, error) {
	var out struct {
		Installations []Installation `json:"installations"`
	}
	q := url.Values{"per_page": {"100"}}
	_, err := c.do(ctx, call{method: "GET", path: "/user/installations", token: userToken, query: q}, &out)
	return out.Installations, err
}
