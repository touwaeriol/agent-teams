import { describe, expect, it } from "vitest";
import { classifyLink, isSameOrigin } from "./navigation";

const app = "https://agent.prophey.ai";

describe("classifyLink", () => {
  it.each([
    ["https://agent.prophey.ai/projects/p1/threads/3/full", "app"],
    ["https://agent.prophey.ai", "app"],
    ["https://agent.prophey.ai:443/login", "app"],
    ["https://github.com/touwaeriol/agent-teams/pull/1", "browser"],
    ["http://agent.prophey.ai/login", "browser"], // other scheme = other origin
    ["https://agent.prophey.ai.evil.example/", "browser"],
    ["https://rustfs.prophey.ai/bucket/x", "browser"],
    ["file:///etc/passwd", "deny"],
    ["javascript:alert(1)", "deny"],
    ["mailto:a@b.c", "deny"],
    ["agentteams://projects/x", "deny"],
    ["about:blank", "deny"],
    ["", "deny"],
  ])("%s → %s", (url, want) => expect(classifyLink(url, app)).toBe(want));
});

it("isSameOrigin works for a local dev server", () => {
  expect(isSameOrigin("http://127.0.0.1:5173/sessions/new", "http://127.0.0.1:5173")).toBe(true);
  expect(isSameOrigin("http://127.0.0.1:5174/", "http://127.0.0.1:5173")).toBe(false);
});
