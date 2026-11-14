// Compile-only check (npm run typecheck): the generated types describe real frames.
import { MESSAGE_META, TOOL_META, type Message, type MessageType, type ToolInputs } from "../gen/ts/index.js";
import type { OfficeLayout, SessionEvent } from "../gen/ts/index.js";

const open: Message<"session.open"> = {
  v: 2,
  type: "session.open",
  id: "c_01",
  seq: 1,
  ts: "2026-11-14T12:00:00Z",
  body: {
    session_id: "s_1",
    open_gen: 1,
    employee: { name: "dev-1", tier: "member", project_id: "p", thread_no: 3 },
    runtime: "claude",
    cwd: "projects/p/threads/3/dev-1",
    resume: null,
    claude: {
      model: "claude-opus-5-5",
      tools: [],
      allowed_tools: ["mcp__platform__*"],
      permission_mode: "default",
      setting_sources: [],
    },
    platform_tools: [{ name: "set_status", description: TOOL_META.set_status.description, input_schema: TOOL_META.set_status.input_schema, blocking: false }],
  },
};

function describe(msg: Message): string {
  switch (msg.type) {
    case "session.event":
      return eventLabel(msg.body);
    case "tool.call":
      return msg.body.tool;
    default:
      return `${msg.type} reliable=${MESSAGE_META[msg.type].reliable}`;
  }
}

function eventLabel(e: SessionEvent): string {
  return e.kind === "tool_started" ? `${e.tool ?? ""}: ${e.summary ?? ""}` : e.kind;
}

const input: ToolInputs["assign_task"] = { assignee: "dev", instruction: "x", main: "API", kind: "fix" };
const types: MessageType[] = Object.keys(MESSAGE_META) as MessageType[];
declare const layout: OfficeLayout;

export const _checks = [describe(open), input, types, layout?.id];
