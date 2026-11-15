package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Do calls any REST endpoint (path relative to BaseURL, e.g. "/repos/o/r/merges")
// and decodes a non-empty 2xx body into out. It returns the HTTP status; a
// status >= 400 comes back as *Error with that status. Used by the delivery
// engine (refs, merges, pull requests, check runs, contents).
func (c *Client) Do(ctx context.Context, method, path, token string, body, out any) (int, error) {
	req, err := c.request(ctx, call{method: method, path: path, token: token, body: body})
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, &Error{Status: 503, Message: fmt.Sprintf("连不上 GitHub：%v", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, restError(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.Unmarshal(raw, out)
}

// restError keeps GitHub's own message (needed to tell "merge conflict" from "not fast-forward").
func restError(code int, raw []byte) error {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Message == "" {
		return statusError(code, raw)
	}
	return &Error{Status: code, Message: fmt.Sprintf("GitHub 返回 %d：%s", code, body.Message)}
}
