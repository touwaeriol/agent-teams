package orchestrator

import (
	"context"
	"errors"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
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
	// WholeThread (thread conversations): every employee of the thread with the lead's runtime, not only the lead.
	WholeThread bool
}

// ModelResult lists the employees changed and the ones left alone (another runtime).
type ModelResult struct {
	Applied []ModelApplied `json:"applied"`
	Skipped []string       `json:"skipped" doc:"Employees of another runtime (Codex members) that keep their model"`
}

// SetConversationModel changes the model of the conversation's employee: the project coordinator
// model in the coordinator conversation, the lead's (or the whole thread's) in a thread. Running
// sessions switch from their next turn (session.set_mode; Claude: Query.setModel, Codex: the
// next turn's model).
func (o *Orchestrator) SetConversationModel(ctx context.Context, in ModelChange) (ModelResult, error) {
	out := ModelResult{Applied: []ModelApplied{}, Skipped: []string{}}
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		c, err := core.ConversationOfUser(ctx, tx, in.UserID, in.ConvID)
		if err != nil {
			return err
		}
		emp, err := o.targetOf(ctx, tx, c)
		if err != nil {
			return err
		}
		p, err := o.project(ctx, tx, emp.ProjectID)
		if err != nil {
			return err
		}
		if c.Kind == "coordinator" {
			return o.setCoordinatorModel(ctx, tx, emp, p, in.Model, &out)
		}
		return o.setThreadModel(ctx, tx, c.ID, emp, p, in, &out)
	})
	return out, err
}

func (o *Orchestrator) setCoordinatorModel(ctx context.Context, tx *core.Tx, emp core.Employee, p store.Project, model string, out *ModelResult) error {
	if err := tx.Gorm(ctx).Table("at_projects").Where("id = ?", p.ID).Update("coordinator_model", model).Error; err != nil {
		return err
	}
	p.CoordinatorModel = model
	a, err := o.ApplyModel(ctx, tx, emp, p)
	out.Applied = append(out.Applied, a)
	return err
}

func (o *Orchestrator) setThreadModel(ctx context.Context, tx *core.Tx, threadID string, lead core.Employee, p store.Project, in ModelChange, out *ModelResult) error {
	targets := []core.Employee{lead}
	if in.WholeThread {
		all, err := core.ThreadEmployees(ctx, tx, threadID)
		if err != nil {
			return err
		}
		targets = all
	}
	for _, e := range targets {
		if e.Runtime != lead.Runtime {
			out.Skipped = append(out.Skipped, e.Name)
			continue
		}
		if err := tx.Gorm(ctx).Table("at_employees").Where("id = ?", e.ID).Update("model", in.Model).Error; err != nil {
			return err
		}
		e.Model = in.Model
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
	m := Model(e, p, composer.DefaultModel(ctx, tx, p.UserID, e.Runtime))
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
