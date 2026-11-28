// Minimal built-in layout: used when the served layout has an unknown
// `format` or fails validation before any good layout was seen (§6.4).
import { $t } from "@/i18n";
import type { OfficeLayout } from "./types";

const person: OfficeLayout["characters"]["person"] = {
  pixel: 4,
  width: 11,
  anchor: { x: 20, y: 56 },
  colors: { h: "@hair", s: "@skin", e: "#2B2B2B", S: "@skin", c: "@shirt", a: "@accent", p: "@pants", k: "#2B2B2B" },
  parts: {
    head: ["...hhhh....", "..hhhhhh...", "..hssssh...", "..seSSes...", "..ssssss...", "...ssss...."],
    torso: ["..cccccc...", ".cccaaccc..", ".scccccc.s.", ".sccccccs..", "..cccccc..."],
    legsStand: ["..pp..pp...", "..pp..pp...", "..kk..kk..."],
    legsWalkA: ["..pp...pp..", ".pp....pp..", ".kk.....kk."],
    legsWalkB: ["...pp.pp...", "...pp.pp...", "...kk.kk..."],
    legsSit: [".pppppppp..", ".pp....pp..", ".kk....kk.."],
  },
  frames: {
    stand: ["head", "torso", "legsStand"],
    walkA: ["head", "torso", "legsWalkA"],
    walkB: ["head", "torso", "legsWalkB"],
    sit: ["head", "torso", "legsSit"],
  },
  animations: { stand: { frames: ["stand"] }, walk: { frames: ["walkA", "walkB"], fps: 4 }, sit: { frames: ["sit"] } },
  overlays: { raiseHand: { cells: [[10, 1, "s"], [10, 2, "s"], [10, 3, "s"], [10, 4, "s"]], blinkFps: 1.5 } },
};

export const BUILTIN_LAYOUT: OfficeLayout = {
  format: 1,
  id: "builtin",
  name: $t("内置最小布局"),
  canvas: { width: 760, height: 460, pixel: 4, background: "#EDE6D8", frame: { color: "#3A3D48", width: 4, radius: 12 } },
  floors: { wood: { base: "#E9C997", pattern: "plain" }, lounge: { base: "#F3E9DC", pattern: "plain" } },
  sprites: {
    desk: {
      kind: "shapes", w: 72, h: 50,
      shapes: [
        { type: "rect", x: 0, y: 12, w: 72, h: 24, fill: "#B07D4F", stroke: "#7A5534", strokeWidth: 3 },
        { type: "rect", x: 24, y: 0, w: 24, h: 18, fill: "#3B3F4A", stroke: "#23262D", strokeWidth: 3 },
        { type: "rect", x: 28, y: 4, w: 16, h: 10, fill: "@screen" },
      ],
      slots: { screen: "#3B3F4A" },
    },
  },
  characters: { person },
  looks: {
    outfits: {
      tee: { label: $t("T 恤"), colors: { shirt: "@color", accent: "shade(@color,-0.3)", pants: "#2F3A55" } },
      hoodie: { label: $t("卫衣"), colors: { shirt: "@color", accent: "shade(@color,0.25)", pants: "#3B3F4A" } },
      suit: { label: $t("西装"), colors: { shirt: "@color", accent: "#FFFFFF", pants: "#23262D" } },
      coat: { label: $t("白大褂"), colors: { shirt: "#F4F4F2", accent: "@color", pants: "@color" } },
    },
    accessories: { none: { label: $t("无") }, glasses: { label: $t("眼镜"), colors: { S: "#2B2B2B" } }, headset: { label: $t("耳机") }, cap: { label: $t("帽子") } },
    colors: ["#4C7BD0", "#3F8F6B", "#C8622A", "#7A4FD1"],
    hair: ["#3B2A20", "#6B4A2E", "#1F1E1C"],
    defaults: { "*": { color: "#4C7BD0", outfit: "tee", accessory: "none" } },
  },
  rooms: [
    { id: "dev", label: $t("开发区"), kind: "dev", rect: [16, 16, 728, 300], floor: "wood", wall: { color: "#8A6A45", width: 4 }, labelStyle: { bg: "#8A6A45", fg: "#FFF6E6" } },
    { id: "lounge", label: $t("休息区"), kind: "lounge", rect: [16, 332, 728, 112], floor: "lounge", wall: { color: "#B49A7C", width: 4 }, labelStyle: { bg: "#B49A7C", fg: "#FFFFFF" } },
  ],
  threadPods: {
    area: { room: "dev", rect: [24, 48, 712, 260] },
    templates: {
      row: {
        size: [352, 120],
        frame: { color: "#B08A5C", focusColor: "{threadColor}", width: 2, style: "dashed" },
        plate: { at: [6, 6], value: "#{no} {title}", maxChars: 22, bg: "#FFFFFF", border: "{threadColor}", color: "#1F1E1C", size: 10 },
        desks: [
          { id: "d1", sprite: "desk", at: [8, 60], seat: [24, 36], for: "lead" },
          { id: "d2", sprite: "desk", at: [96, 60], seat: [112, 36] },
          { id: "d3", sprite: "desk", at: [184, 60], seat: [200, 36] },
          { id: "d4", sprite: "desk", at: [272, 60], seat: [288, 36] },
        ],
        nav: { nodes: { aisle: [176, 112] }, edges: [], seatLinks: {}, exits: { aisle: "hall" } },
      },
    },
    layout: { mode: "grid", gap: [8, 8], columns: 2, use: [{ maxThreads: 4, template: "row" }], overflow: "paginate" },
    seating: { leadDesk: "d1", order: ["roleSort", "instance"] },
    threadColors: ["#2F6FDB", "#C2570C", "#3F8F6B", "#7A4FD1"],
  },
  anchors: { lounge: { points: [[60, 370], [110, 370], [160, 370], [210, 370], [260, 370]], nav: "hall", overflow: "jitter" } },
  nav: { walkSpeed: 90, nodes: { hall: [380, 320] }, edges: [] },
  states: {
    rules: [
      { match: { tier: "coordinator" }, place: "anchor:lounge", anim: "stand" },
      { match: { state: ["raising_hand"] }, place: "current", anim: "keep", overlay: "raiseHand", bubbleKind: "ask" },
      { match: { state: ["idle", "waiting", "queued"] }, place: "anchor:lounge", anim: "stand" },
      { match: { state: ["offline"] }, place: "seat", anim: "sit", opacity: 0.35, grayscale: true },
      { match: {}, place: "seat", anim: "sit" },
    ],
    walkAnim: "walk",
    maxConcurrentWalks: 12,
  },
  labels: { room: { size: 11, padding: [2, 6], offset: [8, 6] }, nameTag: { size: 9, color: "#FFFFFF", bg: "@tag", offset: [0, 60] } },
  bubbles: { size: 11, maxChars: 24, offset: [0, -82], kinds: { plain: { bg: "#FFFFFF", border: "#3B3A36" }, ask: { bg: "#FFF1E2", border: "#C2570C" } } },
  focus: { dimOpacity: 0.3 },
};
