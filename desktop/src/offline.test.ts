import { expect, it } from "vitest";
import { showsOfflinePage } from "./offline";

it("shows the offline page only for real main-frame failures", () => {
  const site = "https://agent.prophey.ai/sessions/new";
  expect(showsOfflinePage(-106, true, site)).toBe(true); // ERR_INTERNET_DISCONNECTED
  expect(showsOfflinePage(-105, true, "http://127.0.0.1:5173/")).toBe(true); // ERR_NAME_NOT_RESOLVED
  expect(showsOfflinePage(-3, true, site)).toBe(false); // ERR_ABORTED
  expect(showsOfflinePage(-106, false, site)).toBe(false); // an iframe
});

it("never shows the offline page for itself (no reload loop)", () => {
  expect(showsOfflinePage(-6, true, "file:///app.asar/dist/offline.html?url=x")).toBe(false);
  expect(showsOfflinePage(-6, true, "")).toBe(false);
});
