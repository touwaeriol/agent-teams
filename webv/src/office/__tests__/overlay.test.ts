import { describe, expect, it } from "vitest";
import { claimableCount } from "../overlay";

describe("claimableCount", () => {
  const board = { claimable: 5, by_thread: { "12": { pending: 2, done: 7 }, "13": { running: 1 } } };
  it("counts the focused thread's pending tasks", () => {
    expect(claimableCount(board, 12)).toBe(2);
    expect(claimableCount(board, 13)).toBe(0);
    expect(claimableCount(board, 99)).toBe(0);
  });
  it("uses the project total without focus", () => {
    expect(claimableCount(board, null)).toBe(5);
    expect(claimableCount(undefined, null)).toBe(0);
  });
});
