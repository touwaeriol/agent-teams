import { describe, expect, it } from "vitest";
import { allowPermission } from "./permissions";

const app = "https://agent.prophey.ai";
const ask = (permission: string, origin = app, mediaTypes?: string[]) => allowPermission({ permission, origin, mediaTypes }, app);

describe("allowPermission", () => {
  it("grants microphone, notifications and clipboard writes to the site", () => {
    expect(ask("media", `${app}/projects/p/threads/1`, ["audio"])).toBe(true);
    expect(ask("media", app)).toBe(true); // check without a type (device list)
    expect(ask("notifications")).toBe(true);
    expect(ask("clipboard-sanitized-write")).toBe(true);
  });
  it("refuses the camera", () => {
    expect(ask("media", app, ["video"])).toBe(false);
    expect(ask("media", app, ["audio", "video"])).toBe(false);
  });
  it("refuses everything to other origins", () => {
    expect(ask("media", "https://evil.example", ["audio"])).toBe(false);
    expect(ask("notifications", "https://rustfs.prophey.ai")).toBe(false);
    expect(ask("notifications", "file:///")).toBe(false);
    expect(ask("notifications", "")).toBe(false);
  });
  it.each(["geolocation", "clipboard-read", "midi", "midiSysex", "openExternal", "pointerLock", "fullscreen", "hid", "serial", "usb", "display-capture", "idle-detection"])(
    "refuses %s",
    (p) => expect(ask(p)).toBe(false),
  );
});
