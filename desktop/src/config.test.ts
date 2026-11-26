import { describe, expect, it } from "vitest";
import { DEFAULT_APP_URL, desktopUserAgent, resolveAppUrl } from "./config";

describe("resolveAppUrl", () => {
  it("defaults to the production site", () => {
    expect(resolveAppUrl({}).toString()).toBe(`${DEFAULT_APP_URL}/`);
    expect(resolveAppUrl({ AGENT_TEAMS_URL: "  " }).origin).toBe(DEFAULT_APP_URL);
  });
  it("takes AGENT_TEAMS_URL when it is http(s)", () => {
    expect(resolveAppUrl({ AGENT_TEAMS_URL: "http://127.0.0.1:5173" }).origin).toBe("http://127.0.0.1:5173");
  });
  it("ignores other schemes and garbage", () => {
    expect(resolveAppUrl({ AGENT_TEAMS_URL: "file:///etc/passwd" }).origin).toBe(DEFAULT_APP_URL);
    expect(resolveAppUrl({ AGENT_TEAMS_URL: "javascript:alert(1)" }).origin).toBe(DEFAULT_APP_URL);
    expect(resolveAppUrl({ AGENT_TEAMS_URL: "not a url" }).origin).toBe(DEFAULT_APP_URL);
  });
});

it("desktopUserAgent appends the product token", () => {
  expect(desktopUserAgent("Mozilla/5.0 Chrome/140", "0.1.0")).toBe("Mozilla/5.0 Chrome/140 AgentTeamsDesktop/0.1.0");
});
