package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
)

func (s *Scheduler) claimOne(ctx context.Context, threadID string) (bool, error) {
	claimed := false
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.ThreadByID(ctx, tx, threadID)
		if err != nil || (t.Status != "running" && t.Status != "waiting") {
			return ignoreMissing(err)
		}
		tasks, err := pendingTasks(ctx, tx, threadID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			ok, err := s.tryClaim(ctx, tx, t, task)
			if err != nil || ok {
				claimed = ok
				return err
			}
		}
		return nil
	})
	return claimed, err
}

func ignoreMissing(err error) error {
	if err == core.ErrNotFound {
		return nil
	}
	return err
}

func pendingTasks(ctx context.Context, tx *core.Tx, threadID string) ([]core.Task, error) {
	rows, err := tx.Query(ctx, `SELECT conversation_id, id FROM at_tasks WHERE thread_id = $1 AND status = 'pending'
		ORDER BY priority, created_at, substring(id from 2)::int FOR UPDATE SKIP LOCKED LIMIT 50`, threadID)
	if err != nil {
		return nil, err
	}
	var keys [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	out := make([]core.Task, 0, len(keys))
	for _, k := range keys {
		t, err := core.TaskByID(ctx, tx, k[0], k[1])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Scheduler) tryClaim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (bool, error) {
	ok, err := core.DepsSatisfied(ctx, tx, task)
	if err != nil || !ok {
		return false, err
	}
	member, err := idleMember(ctx, tx, t, task)
	if err == core.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.claim(ctx, tx, t, task, member)
}

// Named reports whether a task was assigned to an instance rather than a role.
func Named(task core.Task) bool { return task.Assignee != "" && task.Assignee != task.Role }

func idleMember(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (core.Employee, error) {
	name := ""
	if Named(task) {
		name = task.Assignee
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT e.id FROM at_employees e WHERE e.thread_id = $1 AND e.active AND e.tier = 'member'
		AND (($2 <> '' AND e.name = $2) OR ($2 = '' AND e.role = $3))
		AND NOT EXISTS (SELECT 1 FROM at_tasks x WHERE x.thread_id = $1 AND x.claimed_by = e.name AND x.status IN ('claimed','running'))
		ORDER BY e.instance_no FOR UPDATE SKIP LOCKED LIMIT 1`, t.ID, name, task.Role).Scan(&id)
	if err != nil {
		return core.Employee{}, core.MapErr(err)
	}
	return core.EmployeeByID(ctx, tx, id)
}

// claim marks the task claimed, emits task.claimed and sends the member its brief.
func (s *Scheduler) claim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	task, err := core.UpdateTask(ctx, tx, task, map[string]any{"status": "claimed", "claimed_by": m.Name,
		"claimed_at": time.Now().UTC(), "branch": orchestrator.MemberBranch(t.No, m.Name), "nudges": 0})
	if err != nil {
		return err
	}
	if err := tx.EmitTask(ctx, task); err != nil {
		return err
	}
	if err := tx.EmitActivity(ctx, t.ProjectID, &t.ID, m.Name, fmt.Sprintf("领取 %s：%s", task.ID, clip(task.Instruction, 80))); err != nil {
		return err
	}
	tx.AfterCommit(func(ctx context.Context) {
		s.status.Observe(ctx, m.ID, status.Observation{Kind: status.Claimed, Cause: "task.claimed:" + task.ID,
			TaskID: task.ID, TaskKind: task.Kind})
	})
	if err := s.orch.RefreshWorktree(ctx, tx, m); err != nil {
		return err
	}
	text, err := s.taskBrief(ctx, tx, t, task, m)
	if err != nil {
		return err
	}
	_, err = s.orch.Deliver(ctx, tx, m, orchestrator.Delivery{Text: text, Kind: "task", Priority: "next",
		Record: &core.NewMessage{ConversationID: t.ID, Role: "system", Author: "platform", Kind: "note", Content: text,
			Meta: map[string]any{"to": m.Name, "task_id": task.ID}}})
	return err
}

// taskBrief is the message a member gets with a task: instruction, dependency results, roster and unread mail (§7.3).
func (s *Scheduler) taskBrief(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[Task %s", task.ID)
	if title := mainTitle(ctx, tx, task); title != "" {
		fmt.Fprintf(&b, " · %s", title)
	}
	fmt.Fprintf(&b, " · %s]\n%s\n", task.Kind, task.Instruction)
	if err := depResults(ctx, tx, &b, task); err != nil {
		return "", err
	}
	mail, err := core.PeekUnread(ctx, tx, t.ID, m.Name)
	if err != nil {
		return "", err
	}
	if len(mail) > 0 {
		fmt.Fprintf(&b, "\n你有 %d 条未读留言，可用 read_messages 查看。\n", len(mail))
	}
	fmt.Fprintf(&b, "\n你的分支：%s。做完后调用 complete_task(task_id=\"%s\", summary=…)。", task.Branch, task.ID)
	return b.String(), nil
}

func mainTitle(ctx context.Context, q core.DB, task core.Task) string {
	if task.MainTaskID == nil {
		return ""
	}
	var title string
	_ = q.QueryRow(ctx, `SELECT title FROM at_main_tasks WHERE id = $1`, *task.MainTaskID).Scan(&title)
	return title
}

func depResults(ctx context.Context, tx *core.Tx, b *strings.Builder, task core.Task) error {
	for i, dep := range task.DependsOn {
		d, err := core.TaskByID(ctx, tx, task.ConversationID, dep)
		if err != nil {
			continue
		}
		if i == 0 {
			b.WriteString("\n依赖任务的结果：\n")
		}
		fmt.Fprintf(b, "- %s（%s）：%s\n", d.ID, d.ClaimedBy, clip(d.Summary, 500))
	}
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
