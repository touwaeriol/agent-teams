import { describe, expect, it } from "vitest";
import type { MenuItemConstructorOptions } from "electron";
import { isDevToolsShortcut, menuTemplate } from "./menu";

const noop = () => {};
const build = (platform: NodeJS.Platform, devTools = false) => menuTemplate({ platform, appName: "Agent Teams", devTools, openHome: noop, openWebsite: noop });
const roles = (items: MenuItemConstructorOptions[]): string[] =>
  items.flatMap((i) => [...(i.role ? [i.role] : []), ...(Array.isArray(i.submenu) ? roles(i.submenu) : [])]);

describe("menuTemplate", () => {
  it("has the Edit roles on Mac (Cmd+C/V need them) and an app menu first", () => {
    const menu = build("darwin");
    expect(menu[0]?.label).toBe("Agent Teams");
    expect(roles(menu)).toEqual(expect.arrayContaining(["copy", "paste", "cut", "selectAll", "undo", "redo", "quit"]));
  });
  it("has reload, zoom and full screen everywhere", () => {
    for (const p of ["darwin", "win32"] as const) expect(roles(build(p))).toEqual(expect.arrayContaining(["reload", "zoomIn", "zoomOut", "resetZoom", "togglefullscreen"]));
  });
  it("shows DevTools only in development", () => {
    expect(roles(build("win32"))).not.toContain("toggleDevTools");
    expect(roles(build("win32", true))).toContain("toggleDevTools");
  });
  it("has no Mac app menu on Windows", () => {
    expect(roles(build("win32"))).not.toContain("hideOthers");
  });
});

describe("isDevToolsShortcut", () => {
  const key = (k: Partial<{ key: string; control: boolean; meta: boolean; alt: boolean; shift: boolean; type: string }>) => ({ type: "keyDown", key: "I", control: false, meta: false, alt: false, shift: false, ...k });
  it("is Cmd+Alt+I on Mac and Ctrl+Shift+I elsewhere", () => {
    expect(isDevToolsShortcut(key({ meta: true, alt: true }), "darwin")).toBe(true);
    expect(isDevToolsShortcut(key({ control: true, shift: true }), "win32")).toBe(true);
    expect(isDevToolsShortcut(key({ control: true, shift: true }), "darwin")).toBe(false);
    expect(isDevToolsShortcut(key({ control: true }), "win32")).toBe(false);
    expect(isDevToolsShortcut(key({ control: true, shift: true, type: "keyUp" }), "win32")).toBe(false);
  });
});
