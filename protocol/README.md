# /protocol

JSON Schemas (draft 2020-12) for agent-teams v2, and the TypeScript and Go types generated from them. The schemas are the single source of truth (architecture.md §3.4, §5.4). `docs/v2/protocol.md` explains what they mean.

```
schemas/node/*.json        node WebSocket messages: one file per message body, named after its type
                           (session.open.json, git.result.json, …), plus common.json, envelope.json and
                           message.json (generated: the envelope with the body picked by type)
schemas/tools/*.json       platform tools: the root is the input schema, $defs.<Name>Result is the
                           structured result, x-tool holds tiers / blocking tiers / required role flag
schemas/office/            office-layout.schema.json (copied from docs/v2)
examples/                  valid frames for every message type, frames that must be rejected, tool inputs
gen/bundle.schema.json     all types in one schema (used for TS); bundle.go.schema.json is the Go flavour
gen/manifest.json          message metadata (direction, reliable, idempotency key) and tool metadata,
                           including the self-contained input_schema sent in session.open
gen/ts/                    TypeScript: types.ts, office.ts (json-schema-to-typescript), registry.ts, index.ts
gen/go/                    Go module github.com/touwaeriol/agent-teams/protocol (package protocol)
```

## Message envelope

Every WebSocket text frame is one JSON object:

```json
{"v": 2, "type": "session.send", "id": "c_01JD…", "seq": 1842, "ts": "2026-11-14T12:00:00.123Z",
 "trace": "00-…-01", "body": {"session_id": "s_…", "msg_id": "m_…", "text": "…"}}
```

- `type` selects the body schema `schemas/node/<type>.json`. Message types and field names are the ones in protocol.md.
- `id` starts with `c_` when the console sends it and `n_` when the daemon sends it.
- `seq` appears only on reliable messages (`x-reliable: true`). Those are acknowledged with `ack {seq}` and replayed after a reconnect. Unreliable messages (`hello`, `welcome`, `ack`, `error`, `stream.delta`, `log`, `node.stats`, `cred.*`) must not carry `seq`. `message.json` enforces both rules.
- `x-direction` (`console_to_node`, `node_to_console` or `both`) and `x-key` (the idempotency key fields; `id` means the envelope id) are metadata. Validators ignore them, and the generated registries expose them.
- Receivers ignore unknown fields, so message bodies do not set `additionalProperties: false`. Tool inputs do set it.

Not included, because the cut-over is one-shot (architecture.md §14): protocol 1 frames, the legacy `run.*` messages and the v1 `approve` tool.

## Using the generated code

TypeScript (`gen/ts/index.ts`): `Message<"session.open">` is a typed frame and `Message` is the union of all frames, discriminated on `type`. `MESSAGE_META[type]` gives direction, reliability and key. `ToolInputs` and `ToolResults` give tool types, and `TOOL_META[name]` gives tiers, blocking tiers and `input_schema`. Office types are `OfficeLayout` and the `Office.*` namespace.

Go (`gen/go`; use `replace github.com/touwaeriol/agent-teams/protocol => ../protocol/gen/go`):

- One struct per schema, from go-jsonschema. Required fields and enums are checked in `UnmarshalJSON`. Optional and nullable fields are pointers, and free-form objects are `json.RawMessage`.
- `Messages` / `Tools` registries, `Type…` / `Tool…` constants.
- `Decode(frame)`, `DecodeBody(env)` and `NewEnvelope(type, id, seq, body)`.
- `NewValidator()` (santhosh-tekuri/jsonschema over the embedded schemas, formats asserted) with `ValidateFrame`, `ValidateToolInput` and `ValidateOffice`.
- `PlatformTools(tier, flags...)` builds `session.open.platform_tools`, and `ToolInputSchema(name)` returns one tool's input schema.

`types.gen.go`, `registry.gen.go`, `schemas/` and `manifest.json` in `gen/go` are generated. `doc.go`, `meta.go`, `envelope.go`, `validate.go` and the test are hand-written.

## Regenerating

Edit only `schemas/node/*.json` (except `message.json`), `schemas/tools/*.json` and the examples. Then run from the repository root:

```sh
make gen             # fmt schemas, message.json, bundles, manifest, gen/ts, gen/go (Go runs in docker)
make test-protocol   # node:test (schemas, examples, protocol.md examples), tsc, go vet + go test
make check-protocol  # make gen, then fail if git sees changes under protocol/
```

The steps behind `make gen` are: `node scripts/fmt-schemas.mjs`, `node scripts/bundle.mjs`, `node scripts/gen-ts.mjs`, then go-jsonschema v0.24.1 on `gen/bundle.go.schema.json` (in `golang:1.26`, using the module-cache volume `agent-teams-gomod`), then `node scripts/gen-go.mjs`, then gofmt.

`npm run check:examples` validates every example in `docs/v2/protocol.md`:

- JSON blocks: each `a|b|c` alternative is expanded into its own variant and validated.
- Inline shorthands such as `git.result {job_id, ok, head_sha?}`: every field must exist, and literal values must be valid for that field.
- The `session.event` and node-management tables.

The section on protocol 1 compatibility (§11) is skipped.

Schema conventions: every `$defs` key equals its `title` and is unique across all files, because the bundle lifts every def to the top level. Regex patterns must be RE2-compatible (no lookarounds), so that the Go validator accepts them. Cross-file references are relative (`common.json#/$defs/SessionId`) and resolve against `$id` = `https://agent-teams.dev/schemas/<group>/<file>`.
