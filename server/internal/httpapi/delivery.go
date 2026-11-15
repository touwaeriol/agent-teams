package httpapi

import (
	"context"
	"encoding/json"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Gates are the optional delivery checkpoints.
type Gates struct {
	Review     bool `json:"review"`
	Decision   bool `json:"decision"`
	MergeQueue bool `json:"merge_queue"`
}

// CISettings configure how GitHub Checks gate merges.
type CISettings struct {
	Mode           string   `json:"mode" enum:"checks,off"`
	Required       []string `json:"required"`
	AppearTimeoutS int      `json:"appear_timeout_s" minimum:"0"`
	TimeoutS       int      `json:"timeout_s" minimum:"0"`
	OnMissing      string   `json:"on_missing" enum:"pass,fail"`
}

// Delivery is DeliverySettings of api.md §2.
type Delivery struct {
	Gates              Gates      `json:"gates"`
	CI                 CISettings `json:"ci"`
	MergeMethod        string     `json:"merge_method" enum:"squash,merge,rebase"`
	ApprovalTimeoutS   *int       `json:"approval_timeout_s" nullable:"true"`
	NotifyOnAsk        bool       `json:"notify_on_ask"`
	AllowWorkflowEdits bool       `json:"allow_workflow_edits"`
	ApprovalRouting    string     `json:"approval_routing" enum:"lead_first,user" doc:"lead_first: the thread lead decides permission requests and asks the user when unsure; user: every request asks the user"`
	LeadReviewTimeoutS int        `json:"lead_review_timeout_s" minimum:"10" maximum:"3600" doc:"Seconds the lead has before a request goes to the user"`
}

// GatesPatch, CIPatch and DeliveryPatch change only the fields they carry.
type GatesPatch struct {
	Review     *bool `json:"review,omitempty"`
	Decision   *bool `json:"decision,omitempty"`
	MergeQueue *bool `json:"merge_queue,omitempty"`
}

// CIPatch changes CI settings partially.
type CIPatch struct {
	Mode           *string  `json:"mode,omitempty" enum:"checks,off"`
	Required       []string `json:"required,omitempty"`
	AppearTimeoutS *int     `json:"appear_timeout_s,omitempty" minimum:"0"`
	TimeoutS       *int     `json:"timeout_s,omitempty" minimum:"0"`
	OnMissing      *string  `json:"on_missing,omitempty" enum:"pass,fail"`
}

// DeliveryPatch changes delivery settings partially.
type DeliveryPatch struct {
	Gates              *GatesPatch `json:"gates,omitempty"`
	CI                 *CIPatch    `json:"ci,omitempty"`
	MergeMethod        *string     `json:"merge_method,omitempty" enum:"squash,merge,rebase"`
	ApprovalTimeoutS   *int        `json:"approval_timeout_s,omitempty" minimum:"0"`
	NotifyOnAsk        *bool       `json:"notify_on_ask,omitempty"`
	AllowWorkflowEdits *bool       `json:"allow_workflow_edits,omitempty"`
	ApprovalRouting    *string     `json:"approval_routing,omitempty" enum:"lead_first,user"`
	LeadReviewTimeoutS *int        `json:"lead_review_timeout_s,omitempty" minimum:"10" maximum:"3600"`
}

func builtinDelivery() Delivery {
	return Delivery{Gates: Gates{Review: true, MergeQueue: true},
		CI:          CISettings{Mode: "checks", Required: []string{}, AppearTimeoutS: 300, TimeoutS: 3600, OnMissing: "pass"},
		MergeMethod: "squash", NotifyOnAsk: true, ApprovalRouting: core.RouteLeadFirst, LeadReviewTimeoutS: core.DefaultLeadReviewSecs}
}

// deliveryDefault is the admin default (at_settings.delivery_default) over the built-in one.
func (s *Server) deliveryDefault(ctx context.Context) Delivery {
	d := builtinDelivery()
	var raw json.RawMessage
	if found, err := s.store.Setting(ctx, "delivery_default", &raw); err == nil && found {
		d = overlay(d, raw)
	}
	return d
}

// overlay decodes raw JSON over d; unknown or broken input leaves d unchanged.
func overlay(d Delivery, raw []byte) Delivery {
	if len(raw) == 0 {
		return d
	}
	next := d
	next.CI.Required = append([]string(nil), d.CI.Required...)
	if err := json.Unmarshal(raw, &next); err != nil {
		return d
	}
	if next.CI.Required == nil {
		next.CI.Required = []string{}
	}
	return next
}

func applyPatch(d Delivery, p *DeliveryPatch) Delivery {
	if p == nil {
		return d
	}
	raw, _ := json.Marshal(p)
	return overlay(d, raw)
}
