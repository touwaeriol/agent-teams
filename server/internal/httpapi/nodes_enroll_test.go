package httpapi

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

// TestEnrollmentUsesNodeURL: with AGENT_TEAMS_NODE_URL set, the install command points the
// daemon at it instead of the web app's public URL.
func TestEnrollmentUsesNodeURL(t *testing.T) {
	s := &Server{cfg: config.Config{PublicURL: "https://agent.example.com", NodeURL: "https://agent-backend.example.com"}}
	if e := s.enrollment("n1", "tok"); !strings.Contains(e.Install, "--console https://agent-backend.example.com") || strings.Contains(e.Install, "https://agent.example.com") {
		t.Errorf("install: %s", e.Install)
	}
	s.cfg.NodeURL = ""
	if e := s.enrollment("n1", "tok"); !strings.Contains(e.Install, "--console https://agent.example.com") {
		t.Errorf("fallback: %s", e.Install)
	}
}
