package core

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm/clause"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// SessionHold is an at_session_holds row: the session an employee could not open because no
// candidate node has its runtime installed and logged in. First is the first message the session
// will open with (later messages are appended to it), Resume the conversation it resumes, More
// the msg ids merged into First after the first one, Reason what the user is told.
type SessionHold struct {
	EmployeeID string `gorm:"primaryKey"`
	ProjectID  string
	ThreadID   *string // the thread the employee worked in when held (nil: the coordinator)
	Reason     string
	First      protocol.FirstMessage `gorm:"serializer:jsonb"`
	Resume     *protocol.ResumeRef   `gorm:"serializer:json"`
	More       []string              `gorm:"serializer:jsonb"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName maps SessionHold to at_session_holds.
func (SessionHold) TableName() string { return "at_session_holds" }

// maxHeldImages bounds the images a held first message collects.
const maxHeldImages = 8

// HoldOf loads an employee's hold (ErrNotFound when it has none).
func HoldOf(ctx context.Context, q DB, employeeID string) (SessionHold, error) {
	return take[SessionHold](q.Gorm(ctx).Where("employee_id = ?", employeeID))
}

// LockHold loads an employee's hold FOR UPDATE (ErrNotFound when it has none).
func LockHold(ctx context.Context, tx *Tx, employeeID string) (SessionHold, error) {
	return take[SessionHold](tx.Gorm(ctx).Clauses(ForUpdate).Where("employee_id = ?", employeeID))
}

// HoldSession records first as the employee's held session, or appends it to the hold it has in
// the thread it works in now (e pinned with its work thread); a hold from another thread is
// replaced. The reason is replaced either way.
func HoldSession(ctx context.Context, tx *Tx, e Employee, first protocol.FirstMessage, resume *protocol.ResumeRef, reason string) error {
	h, err := LockHold(ctx, tx, e.ID)
	if err == nil && !h.InThread(e.WorkThreadID()) {
		err = ErrNotFound
	}
	switch {
	case err == ErrNotFound:
		h = SessionHold{EmployeeID: e.ID, ProjectID: e.ProjectID, ThreadID: e.WorkThreadID(), First: first, Resume: resume,
			More: []string{}, CreatedAt: time.Now().UTC()}
	case err != nil:
		return err
	default:
		h.First.Text += "\n\n---\n" + first.Text
		h.First.Images = append(h.First.Images, first.Images...)
		if len(h.First.Images) > maxHeldImages {
			h.First.Images = h.First.Images[:maxHeldImages]
		}
		h.More = append(h.More, string(first.MsgID))
	}
	h.Reason, h.UpdatedAt = reason, time.Now().UTC()
	return tx.Gorm(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&h).Error
}

// SetHoldReason updates the reason of a hold that is still waiting.
func SetHoldReason(ctx context.Context, tx *Tx, employeeID, reason string) error {
	return tx.Gorm(ctx).Table("at_session_holds").Where("employee_id = ? AND reason <> ?", employeeID, reason).
		Updates(map[string]any{"reason": reason, "updated_at": time.Now().UTC()}).Error
}

// InThread reports whether the hold belongs to the thread (nil: the coordinator's scope).
func (h SessionHold) InThread(threadID *string) bool {
	if h.ThreadID == nil || threadID == nil {
		return h.ThreadID == nil && threadID == nil
	}
	return *h.ThreadID == *threadID
}

// DropThreadHolds deletes the holds of a thread (it ended) and returns their employees.
func DropThreadHolds(ctx context.Context, tx *Tx, threadID string) ([]string, error) {
	var ids []string
	err := tx.Gorm(ctx).Raw(`DELETE FROM at_session_holds WHERE thread_id = ? RETURNING employee_id`, threadID).Scan(&ids).Error
	return ids, err
}

// DropHold deletes an employee's hold.
func DropHold(ctx context.Context, tx *Tx, employeeID string) error {
	return tx.Gorm(ctx).Where("employee_id = ?", employeeID).Delete(&SessionHold{}).Error
}

// UserHolds lists the employees with a held session in the user's projects, oldest first.
func UserHolds(ctx context.Context, q DB, userID string) ([]SessionHold, error) {
	var out []SessionHold
	err := q.Gorm(ctx).Where("project_id IN (SELECT id FROM at_projects WHERE user_id = ?)", userID).
		Order("created_at, employee_id").Find(&out).Error
	return out, err
}

// SaveHold writes a hold's fields back to its row (a release that had to hold again restores
// the merged msg ids and the original created_at).
func SaveHold(ctx context.Context, tx *Tx, h SessionHold) error {
	first, err := json.Marshal(h.First)
	if err != nil {
		return err
	}
	more, err := json.Marshal(nonNilStrings(h.More))
	if err != nil {
		return err
	}
	return tx.Gorm(ctx).Table("at_session_holds").Where("employee_id = ?", h.EmployeeID).Updates(map[string]any{
		"thread_id": h.ThreadID, "reason": h.Reason, "first": string(first), "more": string(more),
		"created_at": h.CreatedAt, "updated_at": time.Now().UTC()}).Error
}
