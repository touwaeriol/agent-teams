// Package protocol holds the agent-teams v2 wire types shared by the Go console:
// node WebSocket messages, platform tool inputs/results and their JSON Schemas.
//
// types.gen.go and registry.gen.go are generated from protocol/schemas (run `make gen`);
// the schemas directory and manifest.json are copies embedded for runtime validation.
// meta.go, envelope.go and validate.go are hand-written.
package protocol
