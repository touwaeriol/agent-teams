// merge_policy (architecture.md §8.4): under pr and auto a thread in review waits on its PR's
// checks, not on the user; manual (and a failed merge under any policy) waits on the user.
import { describe, expect, it } from "vitest";
import { awaitsAcceptance, showReviewCard, threadKind, threadLine } from "@/lib/present";
import { listGroupOf } from "@/lib/threadList";
import { waitingSummary } from "@/pages/main/logic";
import { thread } from "./fixtures";

const review = (merge_policy: "pr" | "auto" | "manual", patch = {}) =>
  thread(1, { status: "review", merge_policy, pr_number: 7, pr_state: "open", ...patch });

describe("merge_policy", () => {
  it("only manual review (or a failed merge) waits on the user", () => {
    expect(awaitsAcceptance(review("manual"))).toBe(true);
    expect(awaitsAcceptance(review("pr"))).toBe(false);
    expect(awaitsAcceptance(review("auto"))).toBe(false);
    expect(awaitsAcceptance(review("auto", { merge_error: "合并失败：冲突" }))).toBe(true);
    expect(awaitsAcceptance(thread(1, { status: "running", merge_policy: "manual" }))).toBe(false);
  });
  it("pr and auto review threads are in progress, not 等你处理", () => {
    expect(listGroupOf(review("pr"))).not.toBe("waiting");
    expect(listGroupOf(review("manual"))).toBe("waiting");
    expect(threadKind(review("pr"))).toBe("doing");
    expect(threadKind(review("manual"))).toBe("review");
    expect(waitingSummary([review("pr"), review("auto")]).count).toBe(0);
    expect(waitingSummary([review("manual")]).count).toBe(1);
  });
  it("the acceptance card shows for manual, a failed merge and a merge in progress", () => {
    expect(showReviewCard(review("manual"))).toBe(true);
    expect(showReviewCard(review("pr"))).toBe(false);
    expect(showReviewCard(review("pr", { pr_state: "accepting" }))).toBe(true);
  });
  it("lines say what the thread waits for, and a finished pr thread's PR is still open", () => {
    expect(threadLine(review("pr"))).toBe("PR #7 等检查通过");
    expect(threadLine(review("auto"))).toBe("PR #7 检查通过后自动合并");
    expect(threadLine(review("manual"))).toBe("PR #7 等你验收");
    expect(threadLine(thread(1, { status: "done", merge_policy: "pr", pr_number: 7, pr_state: "open" }))).toBe("PR #7 已提交，等合并");
  });
});
