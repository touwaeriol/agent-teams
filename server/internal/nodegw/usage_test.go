package nodegw

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

func TestUsageFresh(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	u := &platform.NodeUsage{Claude: &platform.RuntimeUsage{ReceivedAt: at.Add(-10 * time.Minute)},
		Codex: &platform.RuntimeUsage{ReceivedAt: at}}
	if !usageFresh(u, at.Add(59*time.Second)) {
		t.Fatal("59 s should hit the cache")
	}
	if usageFresh(u, at.Add(61*time.Second)) {
		t.Fatal("61 s should ask the node again")
	}
	if usageFresh(nil, at) || usageFresh(&platform.NodeUsage{}, at) {
		t.Fatal("no report is never fresh")
	}
}

func TestSanitizeUsageLabel(t *testing.T) {
	long := strings.Repeat("模", 50)
	short := " Fable "
	b := &protocol.NodeUsage{Runtimes: protocol.NodeUsageRuntimes{Claude: &protocol.RuntimeUsage{Auth: "subscription", Status: "ok",
		Windows: []protocol.UsageWindow{{Key: "weekly_model", Label: &long, UsedPercent: 5}, {Key: "weekly_model", Label: &short}, {Key: "weekly"}}}}}
	w := sanitizeUsage(b, time.Now()).Claude.Windows
	if got := []rune(w[0].Label); len(got) != 40 || w[0].Key != "weekly_model" {
		t.Fatalf("label %q (%d chars)", w[0].Label, len(got))
	}
	if w[1].Label != "Fable" || w[2].Label != "" {
		t.Fatalf("labels %+v", w)
	}
}
