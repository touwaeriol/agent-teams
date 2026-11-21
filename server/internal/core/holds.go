package core

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// SessionHold is an at_session_holds row: the session an employee could not open because no
// candidate node has its runtime installed and logged in. First is the first message the session
// will open with (later messages are appended to it), Resume the conversation it resumes, More
// the msg ids merged into First after the first one, Reason what the user is told.
type SessionHold struct {
	EmployeeID string
	ProjectID  string
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

// HoldSession records first as the employee's held session, or appends it to the hold it has;
// the reason is replaced either way.
func HoldSession(ctx context.Context, tx *Tx, e Employee, first protocol.FirstMessage, resume *protocol.ResumeRef, reason string) error {
	h, err := LockHold(ctx, tx, e.ID)
	switch {
	case err == ErrNotFound:
		h = SessionHold{EmployeeID: e.ID, ProjectID: e.ProjectID, First: first, Resume: resume, More: []string{}}
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

// SaveHold writes a hold back as it is (a release that found no node yet).
func SaveHold(ctx context.Context, tx *Tx, h SessionHold) error {
	h.UpdatedAt = time.Now().UTC()
	return tx.Gorm(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&h).Error
}
