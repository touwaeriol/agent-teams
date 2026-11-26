import { describe, expect, it } from "vitest";
import { deepLinkToUrl, findDeepLink } from "./deeplink";

const app = "https://agent.prophey.ai";

describe("deepLinkToUrl", () => {
  it.each([
    ["agentteams://projects/p1/threads/12", `${app}/projects/p1/threads/12`],
    ["agentteams://projects/p1/threads/12/full", `${app}/projects/p1/threads/12/full`],
    ["agentteams:///projects/p1/deliverables", `${app}/projects/p1/deliverables`],
    ["agentteams:projects/p1", `${app}/projects/p1`],
    ["AgentTeams://sessions/new?x=1#top", `${app}/sessions/new?x=1#top`],
    ["agentteams://", `${app}/`],
    // Stays on the site whatever the path looks like.
    ["agentteams:////evil.example/x", `${app}/evil.example/x`],
    ["agentteams://@evil.example", `${app}/@evil.example`],
    ["agentteams://%2F%2Fevil.example", `${app}/%2F%2Fevil.example`],
  ])("%s → %s", (link, want) => expect(deepLinkToUrl(link, app)).toBe(want));

  it.each([
    "https://agent.prophey.ai/projects",
    "agentteams://https://evil.example/",
    "agentteams://javascript:alert(1)",
    "agentteams://\\\\evil.example",
    "agentteams://projects/\nx",
    "agentteams://projects/\tx",
    `agentteams://${"a".repeat(3000)}`,
    "other://projects/p1",
  ])("rejects %s", (link) => expect(deepLinkToUrl(link, app)).toBeNull());
});

it("findDeepLink picks the link out of a Windows command line", () => {
  expect(findDeepLink(["C:\\Agent Teams.exe", "--allow-file-access-from-files", "agentteams://projects/p1/threads/2"])).toBe("agentteams://projects/p1/threads/2");
  expect(findDeepLink(["C:\\Agent Teams.exe"])).toBeUndefined();
});
