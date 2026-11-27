package httpapi

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

// TestEnrollmentUsesNodeURL: with AGENT_TEAMS_NODE_URL set, every enrollment snippet points the
// daemon at it instead of the web app's public URL.
func TestEnrollmentUsesNodeURL(t *testing.T) {
	s := &Server{cfg: config.Config{PublicURL: "https://agent.example.com", NodeURL: "https://agent-backend.example.com", NodeImage: "img"}}
	e := s.enrollment("n1", "tok")
	for name, snippet := range map[string]string{"install": e.Install, "docker": e.Docker, "compose": e.Compose} {
		if !strings.Contains(snippet, "agent-backend.example.com") || strings.Contains(snippet, "https://agent.example.com") {
			t.Errorf("%s: %s", name, snippet)
		}
	}
	s.cfg.NodeURL = ""
	if e := s.enrollment("n1", "tok"); !strings.Contains(e.Install, "--console https://agent.example.com") {
		t.Errorf("fallback: %s", e.Install)
	}
}
