// Package httpapi is the REST surface of the control plane (chi + huma v2):
// accounts, admin, projects, Git connections and the GitHub App flow, nodes,
// role templates, office layouts, the node installer and the web app.
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// FieldError is one validation problem (api.md §0: fields [{path, message}]).
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// APIError keeps the v1 error shape {"detail": "..."} and adds a machine code.
type APIError struct {
	Status int          `json:"-"`
	Detail string       `json:"detail" doc:"Human-readable message (Chinese)"`
	Code   string       `json:"code,omitempty" doc:"Machine-readable code such as node_quota"`
	Fields []FieldError `json:"fields,omitempty"`
	header http.Header
}

func (e *APIError) Error() string { return e.Detail }

// GetHeaders implements huma.HeadersError (Retry-After on 429).
func (e *APIError) GetHeaders() http.Header { return e.header }

// GetStatus implements huma.StatusError.
func (e *APIError) GetStatus() int { return e.Status }

// ContentType keeps errors as plain JSON (not problem+json) like v1.
func (e *APIError) ContentType(string) string { return "application/json" }

func fail(status int, code, detail string) error {
	return &APIError{Status: status, Code: code, Detail: detail}
}

func invalid(fields []FieldError, detail string) error {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation", Detail: detail, Fields: fields}
}

var defaultCodes = map[int]string{
	400: "bad_request", 401: "unauthorized", 403: "forbidden", 404: "not_found", 405: "method_not_allowed",
	409: "conflict", 413: "too_large", 422: "validation", 429: "rate_limited", 500: "internal", 503: "unavailable",
}

var defaultDetails = map[int]string{
	400: "请求不正确", 401: "请先登录", 403: "没有权限", 404: "没有找到", 409: "状态冲突",
	422: "参数不正确", 429: "请求太频繁，请稍后再试", 500: "服务器出错了", 503: "依赖的服务暂时不可用",
}

func init() {
	huma.NewError = newHumaError
}

// newHumaError turns huma's own errors (validation, parsing) into APIError.
func newHumaError(status int, msg string, errs ...error) huma.StatusError {
	e := &APIError{Status: status, Code: defaultCodes[status], Detail: detailFor(status, msg)}
	for _, err := range errs {
		if err == nil {
			continue
		}
		e.Fields = append(e.Fields, fieldOf(err))
	}
	if len(e.Fields) > 0 && status == http.StatusUnprocessableEntity {
		e.Detail = fmt.Sprintf("参数不正确：%s %s", e.Fields[0].Path, e.Fields[0].Message)
	}
	return e
}

func detailFor(status int, msg string) string {
	if d, ok := defaultDetails[status]; ok && (msg == "" || isEnglish(msg)) {
		return d
	}
	if msg == "" {
		return http.StatusText(status)
	}
	return msg
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
