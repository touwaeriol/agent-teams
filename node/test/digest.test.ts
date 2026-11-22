import { describe, expect, it } from "vitest";
import { conversationDigest, MAX_DIGEST } from "../src/move/digest.js";

const j = (v: unknown) => JSON.stringify(v);

describe("conversationDigest", () => {
  it("reads todos, files, commands and the recent exchange from a Claude transcript", () => {
    const lines = [
      j({ type: "user", message: { role: "user", content: "实现登录页" } }),
      j({ type: "assistant", message: { role: "assistant", content: [
        { type: "text", text: "先看一下现有路由。" },
        { type: "tool_use", name: "Bash", input: { command: "ls src" } },
        { type: "tool_use", name: "TodoWrite", input: { todos: [{ content: "旧计划", status: "pending" }] } },
      ] } }),
      j({ type: "user", message: { role: "user", content: [{ type: "tool_result", content: "secret tool output" }] } }),
      j({ type: "assistant", isSidechain: true, message: { role: "assistant", content: [{ type: "text", text: "subagent chatter" }] } }),
      j({ type: "assistant", message: { role: "assistant", content: [
        { type: "tool_use", name: "Edit", input: { file_path: "src/login.vue" } },
        { type: "tool_use", name: "TodoWrite", input: { todos: [{ content: "写登录页", status: "completed" }, { content: "加测试", status: "in_progress" }] } },
        { type: "text", text: "登录页写好了，接下来加测试。" },
      ] } }),
      "not json",
    ].join("\n");
    const d = conversationDigest("claude", lines);
    expect(d).toContain("- [completed] 写登录页\n- [in_progress] 加测试");
    expect(d).not.toContain("旧计划");
    expect(d).toContain("- src/login.vue");
    expect(d).toContain("- ls src");
    expect(d).toContain("- 收到：实现登录页");
    expect(d).toContain("- 你：登录页写好了，接下来加测试。");
    expect(d).not.toContain("secret tool output");
    expect(d).not.toContain("subagent chatter");
  });

  it("reads a Codex rollout", () => {
    const item = (payload: unknown) => j({ type: "response_item", payload });
    const lines = [
      item({ type: "message", role: "user", content: [{ type: "input_text", text: "<environment_context>cwd</environment_context>" }] }),
      item({ type: "message", role: "user", content: [{ type: "input_text", text: "修一下 bug" }] }),
      item({ type: "function_call", name: "shell", arguments: j({ command: ["bash", "-lc", "go test ./..."] }) }),
      item({ type: "function_call", name: "apply_patch", arguments: j({ input: "*** Begin Patch\n*** Update File: server/main.go\n@@\n*** End Patch" }) }),
      item({ type: "function_call", name: "update_plan", arguments: j({ plan: [{ step: "复现", status: "completed" }, { step: "修复", status: "in_progress" }] }) }),
      item({ type: "message", role: "assistant", content: [{ type: "output_text", text: "已经定位到原因。" }] }),
    ].join("\n");
    const d = conversationDigest("codex", lines);
    expect(d).toContain("- [in_progress] 修复");
    expect(d).toContain("- server/main.go");
    expect(d).toContain("- go test ./...");
    expect(d).toContain("- 收到：修一下 bug");
    expect(d).toContain("- 你：已经定位到原因。");
    expect(d).not.toContain("environment_context");
  });

  it("keeps the newest turns within the size limit", () => {
    const lines = Array.from({ length: 200 }, (_, i) => j({ type: "user", message: { role: "user", content: `第${i}条 ` + "长".repeat(2000) } }));
    const d = conversationDigest("claude", lines.join("\n"));
    expect(d.length).toBeLessThanOrEqual(MAX_DIGEST);
    expect(d).toContain("第199条");
    expect(d).not.toContain("第100条");
  });

  it("is empty for an unreadable transcript", () => {
    expect(conversationDigest("claude", "garbage\n")).toBe("");
  });
});
