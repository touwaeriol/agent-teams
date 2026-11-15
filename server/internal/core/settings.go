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
}

// builtinDelivery mirrors the REST layer's defaults (httpapi.builtinDelivery).
func builtinDelivery() Delivery {
	var d Delivery
	d.Gates.Review, d.Gates.MergeQueue = true, true
	d.CI.Mode, d.CI.Required, d.CI.AppearTimeoutS, d.CI.TimeoutS, d.CI.OnMissing = "checks", []string{}, 300, 3600, "pass"
	d.MergeMethod, d.NotifyOnAsk = "squash", true
	return d
}

// DeliveryOf returns a project's delivery settings: built-in ← admin default ← project.
func DeliveryOf(ctx context.Context, q DB, projectID string) (Delivery, error) {
	d := builtinDelivery()
	var admin []byte
	err := MapErr(q.QueryRow(ctx, `SELECT value FROM at_settings WHERE key = 'delivery_default'`).Scan(&admin))
	if err != nil && err != ErrNotFound {
		return d, err
	}
	if len(admin) > 0 {
		_ = json.Unmarshal(admin, &d)
	}
	var own []byte
	if err := q.QueryRow(ctx, `SELECT delivery FROM at_projects WHERE id = $1`, projectID).Scan(&own); err != nil {
		return d, MapErr(err)
	}
	_ = json.Unmarshal(own, &d)
	if d.CI.Required == nil {
		d.CI.Required = []string{}
	}
	return d, nil
}
