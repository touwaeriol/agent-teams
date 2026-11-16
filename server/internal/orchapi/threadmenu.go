package orchapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
)

// The thread ⋯ menu's own routes: 添加提醒 (a reminder the timers runner fires once) and
// 发送反馈 (feedback the administrators read under 管理 → 反馈). 置顶 is PATCH pinned.

// MaxReminderAhead bounds how far ahead a reminder may be set.
const MaxReminderAhead = 366 * 24 * time.Hour

type reminderInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Body struct {
		At time.Time `json:"at" doc:"When to remind (in the future, within a year)"`
	}
}

type feedbackInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Body struct {
		Text           string `json:"text" minLength:"1" maxLength:"5000"`
		IncludeContent bool   `json:"include_content,omitempty" doc:"Attach the thread's conversation (latest 100 messages)"`
	}
}

// FeedbackView is one feedback entry for the administrators.
type FeedbackView struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	UserEmail   string     `json:"user_email"`
	ProjectID   string     `json:"project_id"`
	ThreadID    string     `json:"thread_id"`
	ThreadNo    int        `json:"thread_no"`
	ThreadTitle string     `json:"thread_title"`
	Text        string     `json:"text"`
	Content     string     `json:"content" doc:"The thread's conversation when the user included it"`
	HandledAt   *time.Time `json:"handled_at"`
	HandledBy   string     `json:"handled_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

type feedbackOutput struct{ Body FeedbackView }

type feedbackListInput struct {
	Status string `query:"status" enum:"open,handled," doc:"open (default: all)"`
}

type feedbackListOutput struct{ Body []FeedbackView }

type feedbackPatchInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Handled bool `json:"handled"`
	}
}

func (a *API) registerThreadMenu(api huma.API) {
	t := "threads"
	huma.Register(api, op("set-thread-reminder", "PUT", "/api/threads/{tid}/reminder", t,
		summary("Remind me about a thread: at that time it moves under 等你处理 (fires once); replaces any reminder")), a.setReminder)
	huma.Register(api, op("clear-thread-reminder", "DELETE", "/api/threads/{tid}/reminder", t,
		summary("Cancel a pending reminder or clear one that fired")), a.clearReminder)
	huma.Register(api, op("send-thread-feedback", "POST", "/api/threads/{tid}/feedback", t, status(201),
		summary("Send feedback about a thread to the administrators")), a.sendFeedback)
	admin := "admin"
	huma.Register(api, op("admin-list-feedback", "GET", "/api/admin/feedback", admin), a.listFeedback)
	huma.Register(api, op("admin-update-feedback", "PATCH", "/api/admin/feedback/{id}", admin,
		summary("Mark feedback handled (or open again)")), a.updateFeedback)
}

func (a *API) setReminder(ctx context.Context, in *reminderInput) (*threadOutput, error) {
	now := time.Now()
	at := in.Body.At
	if !at.After(now) || at.Sub(now) > MaxReminderAhead {
		return nil, httpapi.Fail(http.StatusUnprocessableEntity, "validation", "提醒时间要在将来一年以内")
	}
	return a.reminder(ctx, in.TID, &at)
}

func (a *API) clearReminder(ctx context.Context, in *tidInput) (*threadOutput, error) {
	return a.reminder(ctx, in.TID, nil)
}

func (a *API) reminder(ctx context.Context, tid string, at *time.Time) (*threadOutput, error) {
	u, t, err := a.thread(ctx, tid)
	if err != nil {
		return nil, err
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadReminder(ctx, tx, t.ID, at); err != nil {
			return err
		}
		if at != nil {
			auditTx(ctx, tx, u.ID, "thread_reminder", t.ID, map[string]any{"at": at.UTC().Format(time.RFC3339)})
		}
		return tx.EmitThread(ctx, t.ID)
	})
	if err != nil {
		return nil, fail(err, "没有这个讨论串")
	}
	if t, err = core.ThreadByID(ctx, a.e.Core.Pool, t.ID); err != nil {
		return nil, err
	}
	v, err := a.threadView(ctx, t)
	return &threadOutput{Body: v}, err
}

// feedbackContentLimit caps the conversation attached to feedback.
const feedbackContentLimit = 200_000

func (a *API) sendFeedback(ctx context.Context, in *feedbackInput) (*feedbackOutput, error) {
	u, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Body.Text)
	if text == "" {
		return nil, httpapi.Fail(http.StatusUnprocessableEntity, "validation", "反馈内容不能为空")
	}
	content := ""
	if in.Body.IncludeContent {
		if content, err = a.transcript(ctx, t); err != nil {
			return nil, err
		}
	}
	f := FeedbackView{ID: core.NewHex(8), UserID: u.ID, UserEmail: u.Email, ProjectID: t.ProjectID, ThreadID: t.ID,
		ThreadNo: t.No, ThreadTitle: t.Title, Text: text, Content: content}
	err = a.e.Core.Pool.Gorm(ctx).Table("at_feedback").Create(map[string]any{"id": f.ID, "user_id": u.ID,
		"project_id": f.ProjectID, "thread_id": f.ThreadID, "thread_no": f.ThreadNo, "thread_title": f.ThreadTitle,
		"text": f.Text, "content": f.Content}).Error
	if err != nil {
		return nil, err
	}
	err = a.e.Core.Pool.Gorm(ctx).Table("at_feedback").Select("created_at").Where("id = ?", f.ID).Row().Scan(&f.CreatedAt)
	return &feedbackOutput{Body: f}, err
}

// transcript is the thread's latest 100 messages as "[时间] 角色：内容" lines.
func (a *API) transcript(ctx context.Context, t core.Thread) (string, error) {
	msgs, err := core.Messages(ctx, a.e.Core.Pool, t.ID, 0, 100)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s\n", t.No, t.Title)
	for _, m := range msgs {
		who := m.Role
		if m.Author != "" {
			who = m.Author
		}
		fmt.Fprintf(&b, "\n[%s] %s：%s\n", m.CreatedAt.UTC().Format("2006-01-02 15:04"), who, m.Content)
	}
	s := b.String()
	if len(s) > feedbackContentLimit {
		s = s[len(s)-feedbackContentLimit:]
	}
	return strings.ToValidUTF8(s, ""), nil
}

func (a *API) feedbackQuery(ctx context.Context) *gorm.DB {
	return a.e.Core.Pool.Gorm(ctx).Table("at_feedback f").Select(`f.id, f.user_id, COALESCE(u.email, '') AS user_email,
		f.project_id, f.thread_id, f.thread_no, f.thread_title, f.text, f.content, f.handled_at, f.handled_by, f.created_at`).
		Joins("LEFT JOIN at_users u ON u.id = f.user_id")
}

func (a *API) listFeedback(ctx context.Context, in *feedbackListInput) (*feedbackListOutput, error) {
	if _, err := a.user(ctx); err != nil {
		return nil, err
	}
	g := a.feedbackQuery(ctx)
	switch in.Status {
	case "open":
		g = g.Where("f.handled_at IS NULL")
	case "handled":
		g = g.Where("f.handled_at IS NOT NULL")
	}
	out := []FeedbackView{}
	err := g.Order("f.created_at DESC").Limit(500).Scan(&out).Error
	return &feedbackListOutput{Body: out}, err
}

func (a *API) updateFeedback(ctx context.Context, in *feedbackPatchInput) (*feedbackOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	set := map[string]any{"handled_at": nil, "handled_by": ""}
	if in.Body.Handled {
		set = map[string]any{"handled_at": time.Now(), "handled_by": u.Email}
	}
	res := a.e.Core.Pool.Gorm(ctx).Table("at_feedback").Where("id = ?", in.ID).Updates(set)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, httpapi.Fail(http.StatusNotFound, "not_found", "没有这条反馈")
	}
	var f FeedbackView
	err = a.feedbackQuery(ctx).Where("f.id = ?", in.ID).Scan(&f).Error
	return &feedbackOutput{Body: f}, err
}
