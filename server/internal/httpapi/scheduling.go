package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// SchedulingSettings are the signed-in user's scheduling thresholds (shared-members.md §4.3).
type SchedulingSettings struct {
	store.QuotaLimits
	Defaults store.QuotaLimits `json:"defaults" doc:"The built-in thresholds (used until the user sets their own)"`
}

type schedulingOutput struct{ Body SchedulingSettings }

type schedulingInput struct{ Body store.QuotaLimits }

func (s *Server) registerScheduling(api huma.API) {
	tags := []string{"auth"}
	huma.Register(api, huma.Operation{OperationID: "get-my-scheduling", Method: "GET", Path: "/api/me/scheduling",
		Summary: "My node scheduling thresholds", Tags: tags}, s.myScheduling)
	huma.Register(api, huma.Operation{OperationID: "put-my-scheduling", Method: "PUT", Path: "/api/me/scheduling",
		Summary: "Set my node scheduling thresholds (percent, 1–100)", Tags: tags}, s.putMyScheduling)
}

func (s *Server) myScheduling(ctx context.Context, _ *struct{}) (*schedulingOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	l, err := s.store.UserQuotaLimits(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	return &schedulingOutput{Body: SchedulingSettings{QuotaLimits: l, Defaults: store.DefaultQuotaLimits()}}, nil
}

func (s *Server) putMyScheduling(ctx context.Context, in *schedulingInput) (*schedulingOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	l := in.Body
	if l.FiveHourPct < 1 || l.FiveHourPct > 100 || l.WeeklyPct < 1 || l.WeeklyPct > 100 {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "阈值要在 1～100 之间")
	}
	if err := s.store.SaveUserQuotaLimits(ctx, p.User.ID, l); err != nil {
		return nil, err
	}
	return &schedulingOutput{Body: SchedulingSettings{QuotaLimits: l, Defaults: store.DefaultQuotaLimits()}}, nil
}
