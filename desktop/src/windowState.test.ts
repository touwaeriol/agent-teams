import { describe, expect, it } from "vitest";
import { DEFAULT_SIZE, parseWindowState, restoreBounds } from "./windowState";

const screen = [{ x: 0, y: 0, width: 1920, height: 1080 }];

describe("parseWindowState", () => {
  it("reads a saved state", () => {
    expect(parseWindowState('{"bounds":{"x":10,"y":20,"width":800,"height":600},"maximized":true}')).toEqual({ bounds: { x: 10, y: 20, width: 800, height: 600 }, maximized: true });
  });
  it.each([null, "", "{", "[]", '{"bounds":{"x":"1"}}', "null"])("survives %j", (text) => expect(parseWindowState(text).bounds).toBeUndefined());
});

describe("restoreBounds", () => {
  it("keeps bounds that are on a display", () => {
    expect(restoreBounds({ x: 100, y: 100, width: 1000, height: 700 }, screen)).toEqual({ x: 100, y: 100, width: 1000, height: 700 });
  });
  it("drops the position of a window on a display that is gone", () => {
    expect(restoreBounds({ x: 3000, y: 100, width: 1000, height: 700 }, screen)).toEqual({ width: 1000, height: 700 });
  });
  it("uses the default size when nothing (sane) is saved", () => {
    expect(restoreBounds(undefined, screen)).toEqual(DEFAULT_SIZE);
    expect(restoreBounds({ x: 0, y: 0, width: 10, height: 10 }, screen)).toEqual(DEFAULT_SIZE);
  });
});
