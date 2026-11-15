import { describe, expect, it } from "vitest";
import { bubbleFor, matchRule, sequenceFor, subjectOf } from "../rules";
import type { EmployeeStatus } from "../types";
import { defaultLayout } from "./fixtures";

const rules = defaultLayout().states.rules;
const pick = (s: EmployeeStatus, tier = "member", role = "dev") => matchRule(rules, subjectOf(s, { tier, role }));

describe("matchRule", () => {
  it("keeps the coordinator at their desk, grey when offline", () => {
    expect(pick({ state: "typing" }, "coordinator", "coordinator")).toMatchObject({ place: "anchor:coordinatorDesk", anim: "typing" });
    expect(pick({ state: "offline" }, "coordinator", "coordinator")).toMatchObject({ grayscale: true, opacity: 0.35 });
    expect(pick({ state: "raising_hand" }, "coordinator", "coordinator")).toMatchObject({ overlay: "raiseHand" });
  });

  it("maps work states to the seat with screen colours", () => {
    expect(pick({ state: "typing" })).toMatchObject({ place: "seat", anim: "typing", screen: "$screenTyping", timer: true });
    expect(pick({ state: "reading" })).toMatchObject({ screen: "$screenReading", anim: "reading" });
    expect(pick({ state: "running" })).toMatchObject({ screen: "$screenRunning" });
    expect(pick({ state: "thinking" })).toMatchObject({ anim: "thinking" });
  });

  it("raising hand stays in place", () => {
    expect(pick({ state: "raising_hand" })).toMatchObject({ place: "current", anim: "keep", overlay: "raiseHand", bubbleKind: "ask" });
  });

  it("decision tasks go to the decision room, CI watchers to the machine room", () => {
    expect(pick({ state: "thinking", task_kind: "decision" }).place).toBe("anchor:decisionSeats");
    expect(pick({ state: "running", substate: "ci" }).place).toBe("anchor:machineWatch");
  });

  it("covers reviewing, committing, merging and rate limits", () => {
    expect(pick({ state: "reading", task_kind: "review" })).toMatchObject({ bubble: "审查中" });
    expect(pick({ state: "running", substate: "git_push" })).toMatchObject({ bubble: "提交代码" });
    expect(pick({ state: "waiting", substate: "merge" })).toMatchObject({ place: "anchor:machineWatch" });
    expect(pick({ state: "thinking", substate: "rate_limited" })).toMatchObject({ place: "anchor:lounge", bubbleKind: "error" });
  });

  it("idle members go to the lounge, waiting to the bench, idle leads stay seated", () => {
    expect(pick({ state: "idle" }).place).toBe("anchor:lounge");
    expect(pick({ state: "waiting" }).place).toBe("anchor:bench");
    expect(pick({ state: "queued" }).place).toBe("anchor:bench");
    expect(pick({ state: "idle" }, "lead", "lead").place).toBe("seat");
  });

  it("uses the status' own tier/role over the employee's", () => {
    expect(matchRule(rules, subjectOf({ state: "typing", tier: "coordinator" }, { tier: "member", role: "dev" })).place).toBe("anchor:coordinatorDesk");
  });
});

describe("bubbleFor", () => {
  const now = Date.parse("2026-11-14T12:00:00Z");
  it("prefers the console bubble, falls back to the rule default", () => {
    const waitRule = pick({ state: "waiting" });
    expect(bubbleFor({ state: "waiting", bubble: "等 t3" }, waitRule, now, 600000)).toEqual({ text: "等 t3", kind: "plain" });
    expect(bubbleFor({ state: "waiting" }, waitRule, now, 600000)).toEqual({ text: "等任务放出", kind: "plain" });
  });

  it("drops an expired set_status bubble but keeps platform bubbles", () => {
    const rule = pick({ state: "idle" });
    const old = { state: "idle" as const, bubble: "喝咖啡", since: "2026-11-14T11:00:00Z", cause: "set_status" };
    expect(bubbleFor(old, rule, now, 600000)!.text).toBe("空闲");
    expect(bubbleFor({ ...old, since: "2026-11-14T11:55:00Z" }, rule, now, 600000)!.text).toBe("喝咖啡");
    expect(bubbleFor({ ...old, cause: "approval.open:ap_1" }, rule, now, 600000)!.text).toBe("喝咖啡");
  });

  it("bubble kind: console non-plain > rule > plain", () => {
    const hand = pick({ state: "raising_hand" });
    expect(bubbleFor({ state: "raising_hand", bubble: "请求权限", bubble_kind: "plain" }, hand, now, 1e9)!.kind).toBe("ask");
    expect(bubbleFor({ state: "raising_hand", bubble: "x", bubble_kind: "error" }, hand, now, 1e9)!.kind).toBe("error");
    expect(bubbleFor({ state: "typing" }, pick({ state: "typing" }), now, 1e9)).toBeUndefined();
  });
});

describe("sequenceFor", () => {
  const seqs = defaultLayout().states.sequences;
  it("triggers claim only for claiming caused by task.claimed", () => {
    expect(sequenceFor(seqs, { state: "claiming", cause: "task.claimed:t8" })).toBeDefined();
    expect(sequenceFor(seqs, { state: "claiming", cause: "set_status" })).toBeUndefined();
    expect(sequenceFor(seqs, { state: "typing", cause: "task.claimed:t8" })).toBeUndefined();
  });
});
