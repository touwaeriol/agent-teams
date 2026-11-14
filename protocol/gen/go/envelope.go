package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// DecodeBody unmarshals the envelope body into the type registered for env.Type.
// The result is a pointer, e.g. *SessionOpen for "session.open".
func DecodeBody(env *Envelope) (any, error) {
	meta, ok := Messages[env.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported message type %q", env.Type)
	}
	body := meta.New()
	if err := json.Unmarshal(env.Body, body); err != nil {
		return nil, fmt.Errorf("%s body: %w", env.Type, err)
	}
	return body, nil
}

// Decode parses a frame into its envelope and typed body.
func Decode(frame []byte) (*Envelope, any, error) {
	var env Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		return nil, nil, err
	}
	body, err := DecodeBody(&env)
	if err != nil {
		return nil, nil, err
	}
	return &env, body, nil
}

// NewEnvelope builds an envelope for a body. seq must be > 0 for reliable types and 0 otherwise.
func NewEnvelope(typ, id string, seq int, body any) (*Envelope, error) {
	meta, ok := Messages[typ]
	if !ok {
		return nil, fmt.Errorf("unsupported message type %q", typ)
	}
	if meta.Reliable != (seq > 0) {
		return nil, fmt.Errorf("%s: reliable=%v but seq=%d", typ, meta.Reliable, seq)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	env := &Envelope{V: 2, Type: typ, ID: id, Ts: time.Now().UTC(), Body: raw}
	if seq > 0 {
		env.Seq = &seq
	}
	return env, nil
}
