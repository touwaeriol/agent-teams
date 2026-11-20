package orchestrator

import (
	"context"
	"errors"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ModelApplied is what /model did to one employee.
type ModelApplied struct {
	Employee string `json:"employee"`
	Model    string `json:"model" doc:"What the employee runs from now on"`
	Live     bool   `json:"live" doc:"Sent to the employee's running session (from its next turn); false: the next session opens with it"`
}

// ModelChange is PUT /api/conversations/{cid}/model.
type ModelChange struct {
	UserID, ConvID string
	// Model is the id to use; "" goes back to what the employee would run without it.
	Model string
	// WholeThread (thread conversations, scope thread): the thread's members with the lead's
	// runtime. Without it the change would be the lead's own, which ErrFixedModel refuses.
	WholeThread bool
}

// ErrFixedModel refuses /model for the coordinator and thread leads: their model is set in their
// role or in the project (Model).
var ErrFixedModel = errors.New("统筹和组长不能用 /model 切换模型，请在岗位配置或项目设置里改")

// ModelResult lists the employees changed and the ones left alone.
type ModelResult struct {
	Applied []ModelApplied `json:"applied"`
	Skipped []string       `json:"skipped" doc:"Employees that keep their model: the lead (its model is set in its role or the project) and members of another runtime"`
}

// SetConversationModel changes the model of a thread's members (scope thread): every member
// (tier member) with the lead's runtime. The coordinator's and the lead's models are set in their
// role or in the project, so the coordinator conversation and the lead scope are refused
// (ErrFixedModel). Running
// sessions switch from their next turn (session.set_mode; Claude: Query.setModel, Codex: the next
// turn's model).
func (o *Orchestrator) SetConversationModel(ctx context.Context, in ModelChange) (ModelResult, error) {
	out := ModelResult{Applied: []ModelApplied{}, Skipped: []string{}}
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		c, err := core.ConversationOfUser(ctx, tx, in.UserID, in.ConvID)
		if err != nil {
			return err
		}
		if c.Kind == "coordinator" || !in.WholeThread {
			return ErrFixedModel
		}
		lead, err := o.targetOf(ctx, tx, c)
		if err != nil {
			return err
		}
		p, err := o.project(ctx, tx, lead.ProjectID)
		if err != nil {
			return err
		}
		return o.setThreadModel(ctx, tx, c.ID, lead, p, in.Model, &out)
	})
	return out, err
}

func (o *Orchestrator) setThreadModel(ctx context.Context, tx *core.Tx, threadID string, lead core.Employee, p store.Project, model string, out *ModelResult) error {
	all, err := core.ThreadParticipants(ctx, tx, threadID)
	if err != nil {
		return err
	}
	for _, e := range all {
		if fixedModel(e.Tier) || e.Runtime != lead.Runtime {
			out.Skipped = append(out.Skipped, e.Name)
			continue
		}
		if err := tx.Gorm(ctx).Table("at_employees").Where("id = ?", e.ID).Update("model", model).Error; err != nil {
			return err
		}
		e.Model = model
		a, err := o.ApplyModel(ctx, tx, e, p)
		if err != nil {
			return err
		}
		out.Applied = append(out.Applied, a)
	}
	return nil
}

// ApplyModel sends the employee's model to its session on a node, if it has one.
//
// m is ” only for a Codex employee with nothing set at any level (DefaultCodexModel). Nothing is
// sent then (Live false): a set_mode with an empty model would be ignored by the node anyway, and
// the running Codex session keeps what it runs; the next session opens without a model, on the
// Codex CLI's own default.
func (o *Orchestrator) ApplyModel(ctx context.Context, tx *core.Tx, e core.Employee, p store.Project) (ModelApplied, error) {
	m := Model(e, p)
	out := ModelApplied{Employee: e.Name, Model: m}
	if m == "" {
		return out, nil
	}
	s, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && s.NodeID == nil) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	_, err = o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionSetMode,
		Body: protocol.SessionSetMode{SessionID: protocol.SessionID(s.ID), Model: &m}, SessionID: s.ID, OpenGen: s.OpenGen})
	out.Live = err == nil
	return out, err
}
