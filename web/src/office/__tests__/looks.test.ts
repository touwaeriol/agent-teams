import { describe, expect, it } from "vitest";
import { shadeColor, resolveColor } from "../color";
import { baseLook, composeFrame, frameRows, resolveLook } from "../looks";
import { animFrame, blinkOn, rackLights, wallSpans } from "../render";
import { defaultLayout } from "./fixtures";

const layout = defaultLayout();
const at = (px: ReturnType<typeof composeFrame>, x: number, y: number) => px.filter((p) => p.x === x && p.y === y).pop()?.color;

describe("colors", () => {
  it("resolves palette, slots, template vars and shade()", () => {
    const ctx = { palette: { wood: "#B07D4F" }, slots: { color: "#4C7BD0" }, vars: { threadColor: "#2F6FDB" } };
    expect(resolveColor("$wood", ctx)).toBe("#B07D4F");
    expect(resolveColor("@color", ctx)).toBe("#4C7BD0");
    expect(resolveColor("{threadColor}", ctx)).toBe("#2F6FDB");
    expect(resolveColor("shade(@color,-0.3)", ctx)).toBe(shadeColor("#4C7BD0", -0.3));
    expect(shadeColor("#808080", -1)).toBe("#000000");
    expect(shadeColor("#808080", 1)).toBe("#ffffff");
  });
});

describe("looks", () => {
  it("fills missing fields from role, tier and * defaults", () => {
    expect(baseLook(layout, { id: "x", name: "qa-1", tier: "member", role: "qa" })).toMatchObject({ color: "#3F8F6B", outfit: "coat", accessory: "glasses" });
    expect(baseLook(layout, { id: "x", name: "pm-1", tier: "member", role: "pm" })).toMatchObject({ outfit: "tee", accessory: "none" });
    expect(baseLook(layout, { id: "x", name: "dev-1", tier: "member", role: "dev", look: { color: "#B7413E", outfit: "hoodie" } })).toMatchObject({ color: "#B7413E", outfit: "hoodie", accessory: "none" });
  });

  it("varies hair and shirt shade by instance unless hair is explicit", () => {
    const d1 = resolveLook(layout, { id: "1", name: "dev-1", tier: "member", role: "dev" });
    const d2 = resolveLook(layout, { id: "2", name: "dev-2", tier: "member", role: "dev" });
    expect(d1.hair).toBe(layout.looks.hair[0]);
    expect(d2.hair).toBe(layout.looks.hair[1]);
    expect(d1.slots.shirt).toBe("#4C7BD0");
    expect(d2.slots.shirt).toBe(shadeColor("#4C7BD0", -0.12));
    const l2 = resolveLook(layout, { id: "3", name: "lead-2", tier: "lead", role: "lead" });
    expect(l2.hair).toBe("#3B2A20");
  });

  it("applies outfit and accessory colours, patches and cells", () => {
    const qa = resolveLook(layout, { id: "q", name: "qa-1", tier: "member", role: "qa" });
    const px = composeFrame(layout, qa, "stand");
    expect(at(px, 4, 3)).toBe("#2B2B2B"); // glasses turn S black
    expect(at(px, 2, 6)).toBe("#F4F4F2"); // white coat
    const coord = resolveLook(layout, { id: "c", name: "leader", tier: "coordinator", role: "coordinator" });
    expect(frameRows(layout.characters.person, "stand", coord.patch)[6]).toBe(".cccccccc..");
    expect(at(composeFrame(layout, coord, "stand"), 1, 2)).toBe("#2B2B2B"); // headset cell
  });

  it("adds overlays and greys out", () => {
    const dev = resolveLook(layout, { id: "d", name: "dev-1", tier: "member", role: "dev" });
    expect(at(composeFrame(layout, dev, "stand", { overlay: "raiseHand" }), 10, 1)).toBe("#F1C9A5");
    const grey = composeFrame(layout, dev, "stand", { grayscale: true });
    for (const p of grey) expect(p.color).toMatch(/^#([0-9a-f]{2})\1\1$/);
  });
});

describe("render helpers", () => {
  it("rack LEDs: active, parked, queued, empty; extra capacity merges into the last", () => {
    expect(rackLights({ id: "n", name: "rc", online: true, capacity: 4, active: 3 }, 4)).toEqual(["active", "active", "active", "empty"]);
    expect(rackLights({ id: "n", name: "cvp", online: true, capacity: 6, active: 3, queued: 2 }, 4)).toEqual(["active", "active", "active", "queued"]);
    expect(rackLights({ id: "n", name: "x", online: false, capacity: 2, active: 0 }, 4)).toEqual(["offline", "offline"]);
  });

  it("cuts door gaps out of walls", () => {
    const dev = layout.rooms.find((r) => r.id === "dev")!;
    expect(wallSpans(dev, "e")).toEqual([[0, 18], [42, 560]]);
    expect(wallSpans(dev, "n")).toEqual([[0, 704]]);
  });

  it("selects animation frames by fps and blinks overlays", () => {
    expect(animFrame(layout, "walk", 0, 0).frame).toBe("walkA");
    expect(animFrame(layout, "walk", 0, 260).frame).toBe("walkB");
    expect(animFrame(layout, "thinking", 0, 600).bob).toBe(-2);
    expect(animFrame(layout, "nope", 0, 0).frame).toBe("stand");
    expect(blinkOn(1.5, 0)).toBe(true);
    expect(blinkOn(1.5, 400)).toBe(false);
  });
});
