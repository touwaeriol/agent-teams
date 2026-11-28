package httpapi

import (
	"errors"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// Errors carry a translatable message next to the Chinese detail: the key keeps its {placeholders}
// and the values come as params, so the web app can show it in the user's language.
func TestErrorMessageIsTranslatable(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("i18n@example.com")
	c.must(201, "POST", "/api/projects", map[string]string{"name": "alpha"}, nil)

	var e APIError
	c.must(409, "POST", "/api/projects", map[string]string{"name": "alpha"}, &e)
	if e.Detail != "已经有叫 alpha 的项目了" || e.Code != "project_exists" {
		t.Fatalf("detail/code = %q %q", e.Detail, e.Code)
	}
	if e.Message == nil || e.Message.Key != "已经有叫 {name} 的项目了" || e.Message.Params["name"] != "alpha" {
		t.Fatalf("message = %+v", e.Message)
	}

	// huma's own validation errors and fixed messages carry a key too.
	e = APIError{}
	c.must(422, "PUT", "/api/me/locale", map[string]string{"locale": "fr"}, &e)
	if e.Message == nil || e.Message.Key == "" {
		t.Fatalf("validation message = %+v", e)
	}
	e = APIError{}
	h.client().must(401, "GET", "/api/projects", nil, &e)
	if e.Message == nil || e.Message.Key != e.Detail {
		t.Fatalf("401 message = %+v", e)
	}
}

func TestInvalidFieldCarriesText(t *testing.T) {
	err := invalidField("role", msg.T("没有这个岗位：{role}", "role", "dev")).(*APIError)
	if err.Detail != "没有这个岗位：dev" || err.Fields[0].Text == nil || err.Fields[0].Text.Params["role"] != "dev" {
		t.Fatalf("got %+v", err)
	}
}

// The helpers for extension routes (orchapi) carry the message key too.
func TestExportedHelpersCarryText(t *testing.T) {
	var e *APIError
	if !errors.As(Invalidf("refs", "没有讨论串 #{no}", "no", 3), &e) || e.Message.Key != "没有讨论串 #{no}" ||
		e.Detail != "没有讨论串 #3" || e.Fields[0].Text == nil {
		t.Fatalf("Invalidf = %+v", e)
	}
	sentinel := msg.Err("节点不在线")
	if !errors.As(FailErr(409, "node_offline", sentinel), &e) || e.Message.Key != "节点不在线" || e.Code != "node_offline" {
		t.Fatalf("FailErr = %+v", e)
	}
}
