// Package httpapi is the REST surface of the control plane (Gin + huma v2, Casbin route authorization):
// accounts, admin, projects, Git connections and the GitHub App flow, nodes,
// role templates, office layouts, the node installer and the web app.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// FieldError is one validation problem (api.md §0: fields [{path, message}]).
type FieldError struct {
	Path    string    `json:"path"`
	Message string    `json:"message"`
	Text    *msg.Text `json:"text,omitempty" doc:"Translatable form of message"`
}

// APIError keeps the v1 error shape {"detail": "..."} and adds a machine code and the
// translatable message the web app shows in the user's language.
type APIError struct {
	Status  int          `json:"-"`
	Detail  string       `json:"detail" doc:"Human-readable message (Chinese)"`
	Code    string       `json:"code,omitempty" doc:"Machine-readable code such as node_quota"`
	Message *msg.Text    `json:"message,omitempty" doc:"Translatable form of detail (key + params)"`
	Fields  []FieldError `json:"fields,omitempty"`
	header  http.Header
}

func (e *APIError) Error() string { return e.Detail }

// GetHeaders implements huma.HeadersError (Retry-After on 429).
func (e *APIError) GetHeaders() http.Header { return e.header }

// GetStatus implements huma.StatusError.
func (e *APIError) GetStatus() int { return e.Status }

// ContentType keeps errors as plain JSON (not problem+json) like v1.
func (e *APIError) ContentType(string) string { return "application/json" }

// fail is an error with a fixed message: detail is also its translation key.
func fail(status int, code, detail string) error {
	return failMsg(status, code, msg.T(detail))
}

// failf is an error with placeholders: failf(409, "node_exists", "已经有叫 {name} 的节点了", "name", n).
func failf(status int, code, key string, kv ...any) error {
	return failMsg(status, code, msg.T(key, kv...))
}

// failErr reports an error from a lower layer, keeping its translatable message when it has one.
func failErr(status int, code string, err error) error {
	return failMsg(status, code, msg.Of(err))
}

func failMsg(status int, code string, m msg.Text) error {
	return &APIError{Status: status, Code: code, Detail: m.String(), Message: &m}
}

// invalidField is a 422 for one field whose problem is the message: invalidField("node_id", msg.T(...)).
func invalidField(path string, m msg.Text) error {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation", Detail: m.String(), Message: &m,
		Fields: []FieldError{{Path: path, Message: m.String(), Text: &m}}}
}

// invalidf is a 422 with field problems and a summary with placeholders.
func invalidf(fields []FieldError, key string, kv ...any) error {
	m := msg.T(key, kv...)
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation", Detail: m.String(), Message: &m, Fields: fields}
}

func (e *APIError) withFields(fields []FieldError) error {
	e.Fields = fields
	return e
}

func invalid(fields []FieldError, detail string) error {
	m := msg.T(detail)
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation", Detail: detail, Message: &m, Fields: fields}
}

var defaultCodes = map[int]string{
	400: "bad_request", 401: "unauthorized", 403: "forbidden", 404: "not_found", 405: "method_not_allowed",
	409: "conflict", 413: "too_large", 422: "validation", 429: "rate_limited", 500: "internal", 503: "unavailable",
}

var defaultDetails = map[int]msg.Text{
	400: msg.T("请求不正确"), 401: msg.T("请先登录"), 403: msg.T("没有权限"), 404: msg.T("没有找到"), 409: msg.T("状态冲突"),
	422: msg.T("参数不正确"), 429: msg.T("请求太频繁，请稍后再试"), 500: msg.T("服务器出错了"), 503: msg.T("依赖的服务暂时不可用"),
}

func init() {
	huma.NewError = newHumaError
}

// newHumaError turns huma's own errors (validation, parsing) into APIError.
func newHumaError(status int, text string, errs ...error) huma.StatusError {
	e := &APIError{Status: status, Code: defaultCodes[status], Detail: detailFor(status, text)}
	m := msg.T(e.Detail)
	e.Message = &m
	for _, err := range errs {
		if err == nil {
			continue
		}
		e.Fields = append(e.Fields, fieldOf(err))
	}
	if len(e.Fields) > 0 && status == http.StatusUnprocessableEntity {
		m := msg.T("参数不正确：{path} {problem}", "path", e.Fields[0].Path, "problem", e.Fields[0].Message)
		e.Detail, e.Message = m.String(), &m
	}
	return e
}

func detailFor(status int, text string) string {
	if d, ok := defaultDetails[status]; ok && (text == "" || isEnglish(text)) {
		return d.Key
	}
	if text == "" {
		return http.StatusText(status)
	}
	return text
}

func isEnglish(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

func fieldOf(err error) FieldError {
	var d *huma.ErrorDetail
	if errors.As(err, &d) {
		return FieldError{Path: strings.TrimPrefix(d.Location, "body."), Message: d.Message}
	}
	return FieldError{Message: err.Error()}
}
