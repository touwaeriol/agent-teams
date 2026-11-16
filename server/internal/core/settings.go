package core

import (
	"context"
	"encoding/json"
)

// Delivery is the project's delivery flow (data-model.md at_projects.delivery) with defaults applied.
type Delivery struct {
	Gates struct {
		Review     bool `json:"review"`
		Decision   bool `json:"decision"`
		MergeQueue bool `json:"merge_queue"`
	} `json:"gates"`
	CI struct {
		Mode           string   `json:"mode"`
		Required       []string `json:"required"`
		AppearTimeoutS int      `json:"appear_timeout_s"`
		TimeoutS       int      `json:"timeout_s"`
		OnMissing      string   `json:"on_missing"`
	} `json:"ci"`
	MergeMethod        string `json:"merge_method"`
	ApprovalTimeoutS   *int   `json:"approval_timeout_s"`
	NotifyOnAsk        bool   `json:"notify_on_ask"`
	AllowWorkflowEdits bool   `json:"allow_workflow_edits"`
	// ApprovalRouting: lead_first (the thread lead decides, unsure → user) or user (every card asks the user).
	ApprovalRouting    string `json:"approval_routing"`
	LeadReviewTimeoutS int    `json:"lead_review_timeout_s"`
}

// Approval routings and the lead's default time to decide.
const (
	RouteLeadFirst        = "lead_first"
	RouteUser             = "user"
	DefaultLeadReviewSecs = 120
)

// builtinDelivery mirrors the REST layer's defaults (httpapi.builtinDelivery).
func builtinDelivery() Delivery {
	var d Delivery
	d.Gates.Review, d.Gates.MergeQueue = true, true
	d.CI.Mode, d.CI.Required, d.CI.AppearTimeoutS, d.CI.TimeoutS, d.CI.OnMissing = "checks", []string{}, 300, 3600, "pass"
	d.MergeMethod, d.NotifyOnAsk = "squash", true
	d.ApprovalRouting, d.LeadReviewTimeoutS = RouteLeadFirst, DefaultLeadReviewSecs
	return d
}

// DeliveryOf returns a project's delivery settings: built-in ← admin default ← project.
func DeliveryOf(ctx context.Context, q DB, projectID string) (Delivery, error) {
	d := builtinDelivery()
	admin, err := take[struct{ Value []byte }](q.Gorm(ctx).Table("at_settings").Select("value").
		Where("key = 'delivery_default'"))
	if err != nil && err != ErrNotFound {
		return d, err
	}
	if len(admin.Value) > 0 {
		_ = json.Unmarshal(admin.Value, &d)
	}
	own, err := take[struct{ Delivery []byte }](q.Gorm(ctx).Table("at_projects").Select("delivery").Where("id = ?", projectID))
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal(own.Delivery, &d)
	if d.CI.Required == nil {
		d.CI.Required = []string{}
	}
	return d, nil
}
