package orchestrator

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Every registered runtime needs its session.open options and its project thread model.
func TestRuntimeOptionsCoverRegistry(t *testing.T) {
	for _, id := range runtimes.IDs() {
		if runtimeOptions[id] == nil {
			t.Errorf("runtimeOptions has no entry for %s", id)
		}
		if usageOf[id] == nil {
			t.Errorf("usageOf has no entry for %s", id)
		}
		if threadModels[id] == nil {
			t.Errorf("threadModels has no entry for %s", id)
		}
	}
	if len(runtimeOptions) != len(runtimes.IDs()) || len(threadModels) != len(runtimes.IDs()) || len(usageOf) != len(runtimes.IDs()) {
		t.Error("an entry names a runtime the registry does not have")
	}
}

// An unknown runtime id opens like the default runtime instead of panicking.
func TestUnknownRuntimeOpensAsDefault(t *testing.T) {
	e := emp("member", "dev", store.Role{})
	e.Runtime = "gemini"
	if got := builtinModel(e.Runtime); got != DefaultModel {
		t.Fatalf("builtinModel(unknown) = %q", got)
	}
	if got := projectModel(e, store.Project{ThreadModel: "m", ThreadCodexModel: "c"}); got != "m" {
		t.Fatalf("projectModel(unknown) = %q", got)
	}
}
