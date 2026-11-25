package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestLegacyFitSaysWhatWasInlined: an old daemon is told a small text file is in the text only when
// inlineTexts really put it there; one it could not read (storage slow or down) is reported as missing.
func TestLegacyFitSaysWhatWasInlined(t *testing.T) {
	o := &Orchestrator{core: &core.Store{}}
	refs := []core.AttachmentRef{
		{ID: "img_a", Name: "a.txt", ContentType: core.TextType, Size: 10},
		{ID: "img_b", Name: "b.txt", ContentType: core.TextType, Size: 10},
	}
	text := "看看" + inlineHeader("a.txt") + "\n```\nhello\n```"
	f, err := o.legacyFit(context.Background(), nil, "p1", text, refs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.text, "img_a a.txt（text/plain，10 B）：内容已放在正文里") {
		t.Fatalf("inlined one: %s", f.text)
	}
	if strings.Contains(f.text, "img_b b.txt（text/plain，10 B）：内容已放在正文里") || !strings.Contains(f.text, "img_b b.txt（text/plain，10 B）：没能放进正文") {
		t.Fatalf("unread one: %s", f.text)
	}
}
