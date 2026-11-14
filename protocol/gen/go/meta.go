package protocol

import "slices"

// Direction says who sends a node message.
type Direction string

const (
	ConsoleToNode Direction = "console_to_node"
	NodeToConsole Direction = "node_to_console"
	Both          Direction = "both"
)

// MessageMeta describes one node message type.
type MessageMeta struct {
	Direction Direction
	// Reliable messages carry seq, are acknowledged and replayed after reconnects.
	Reliable bool
	// Key lists the body fields forming the idempotency key ("id" = envelope id).
	Key []string
	// New returns a pointer to a zero body value of the message's type.
	New func() any
}

// ToolMeta describes one platform tool.
type ToolMeta struct {
	Tiers []Tier
	// Blocking lists the tiers for which the console answers only once a person or teammate replied.
	Blocking []Tier
	// Requires names a role flag the tool depends on (e.g. "can_delegate"), or "".
	Requires  string
	NewInput  func() any
	NewResult func() any // nil when the tool only returns text
}

// AvailableTo reports whether the tool is offered to the tier.
func (m ToolMeta) AvailableTo(t Tier) bool {
	return hasTier(m.Tiers, t)
}

// BlocksFor reports whether the tool blocks for the tier.
func (m ToolMeta) BlocksFor(t Tier) bool {
	return hasTier(m.Blocking, t)
}

func hasTier(list []Tier, t Tier) bool {
	return slices.Contains(list, t)
}
