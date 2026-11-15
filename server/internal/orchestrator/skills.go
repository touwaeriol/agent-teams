package orchestrator

import (
	"context"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/skills"
)

// skillTool is the Agent SDK tool that loads a skill's instructions.
const skillTool = "Skill"

// addSkills puts the role's skills into session.open (architecture.md §6.5): Claude gets
// the bundles (the daemon loads them as a local plugin) plus the Skill tool; Codex has no
// skill loading, so the skill text is appended to its developer instructions.
func (o *Orchestrator) addSkills(ctx context.Context, q core.DB, userID string, e core.Employee, open *protocol.SessionOpen) error {
	if e.Tier == "coordinator" || len(e.Spec.Skills) == 0 {
		return nil
	}
	bundles, err := skills.Bundles(ctx, q, userID, e.Spec.Skills)
	if err != nil || len(bundles) == 0 {
		return err
	}
	applySkills(open, bundles)
	return nil
}

func applySkills(open *protocol.SessionOpen, bundles []protocol.SkillBundle) {
	if open.Codex != nil {
		dev := ""
		if open.Codex.DeveloperInstructions != nil {
			dev = *open.Codex.DeveloperInstructions
		}
		dev += skills.Instructions(bundles)
		open.Codex.DeveloperInstructions = &dev
		return
	}
	if open.Claude == nil {
		return
	}
	open.Skills = bundles
	open.Claude.Tools = mergeLists(open.Claude.Tools, []string{skillTool})
	open.Claude.AllowedTools = mergeLists(open.Claude.AllowedTools, []string{skillTool})
}
