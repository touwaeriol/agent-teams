// State rules (office-layout.md §4.9): first matching rule wins.
import type { BubbleKind, EmployeeStatus, OfficeEmployee, SceneEvent, Sequence, StateRule } from "./types";

export interface RuleSubject {
  state: string;
  substate: string;
  tier: string;
  role: string;
  taskKind: string;
}

export function subjectOf(status: EmployeeStatus, employee?: Pick<OfficeEmployee, "tier" | "role">): RuleSubject {
  return {
    state: status.state,
    substate: status.substate ?? "",
    tier: status.tier || employee?.tier || "",
    role: status.role || employee?.role || "",
    taskKind: status.task_kind ?? "",
  };
}

export function ruleMatches(rule: StateRule, s: RuleSubject): boolean {
  const m = rule.match;
  if (m.state && !m.state.includes(s.state as never)) return false;
  if (m.substate && !m.substate.includes(s.substate)) return false;
  if (m.tier && m.tier !== s.tier) return false;
  if (m.role && !m.role.includes(s.role)) return false;
  if (m.taskKind && !m.taskKind.includes(s.taskKind)) return false;
  return true;
}

const FALLBACK_RULE: StateRule = { match: {}, place: "seat", anim: "sit" };

export function matchRule(rules: StateRule[], s: RuleSubject): StateRule {
  return rules.find((r) => ruleMatches(r, s)) ?? FALLBACK_RULE;
}

export interface Bubble {
  text: string;
  kind: BubbleKind;
}

/**
 * Bubble text priority: the console's bubble (set_status while not expired,
 * else platform generated) > the rule's default. Only set_status bubbles
 * expire (`bubbles.ttlMs` after the event). Kind: the console's non-plain
 * kind > the rule's bubbleKind > plain.
 */
export function bubbleFor(status: EmployeeStatus, rule: StateRule, now: number, ttlMs: number): Bubble | undefined {
  const fromSetStatus = (status.cause ?? "").startsWith("set_status");
  const fresh = status.bubble && !(fromSetStatus && expired(status.since, now, ttlMs));
  const text = fresh ? status.bubble! : rule.bubble ?? "";
  if (!text) return undefined;
  const given = status.bubble_kind && status.bubble_kind !== "plain" ? status.bubble_kind : undefined;
  return { text, kind: given ?? rule.bubbleKind ?? "plain" };
}

function expired(since: string | number | undefined, now: number, ttlMs: number): boolean {
  const t = toMs(since);
  return t !== undefined && now - t > ttlMs;
}

export function toMs(v: string | number | undefined): number | undefined {
  if (v === undefined || v === "") return undefined;
  if (typeof v === "number") return v;
  const t = Date.parse(v);
  return Number.isNaN(t) ? undefined : t;
}

/** The sequence a status change triggers, if any. */
export function sequenceFor(sequences: Record<string, Sequence> | undefined, status: EmployeeStatus): Sequence | undefined {
  for (const seq of Object.values(sequences ?? {})) {
    if (seq.trigger.event || seq.trigger.state !== status.state) continue;
    if (seq.trigger.cause && !(status.cause ?? "").startsWith(seq.trigger.cause)) continue;
    return seq;
  }
  return undefined;
}

/** The sequences a scene event (a new work table, an ended thread) triggers. */
export function eventSequences(sequences: Record<string, Sequence> | undefined, event: SceneEvent): Sequence[] {
  return Object.values(sequences ?? {}).filter((s) => s.trigger.event === event);
}
