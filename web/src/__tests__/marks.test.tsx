import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { CheckMark, LiveDot, StateMark, StatusPill } from "@/components/app/bits";
import { ThreadMark } from "@/components/app/thread";
import { checkLook, taskLook, threadLook } from "@/lib/present";
import { leadTimeoutLabel, LEAD_TIMEOUTS } from "@/pages/ProjectSettings";
import { thread } from "./fixtures";

const spins = (html: string) => html.includes("at-spin");

describe("in-progress marks spin", () => {
  it("draws running tasks, threads and working employees as spinners, not-started ones as static marks", () => {
    expect(spins(renderToStaticMarkup(<StateMark look={taskLook({ status: "running", depends_on: [] })} />))).toBe(true);
    expect(spins(renderToStaticMarkup(<StateMark look={taskLook({ status: "claimed", depends_on: [] })} />))).toBe(true);
    expect(spins(renderToStaticMarkup(<StateMark look={taskLook({ status: "pending", depends_on: [] })} />))).toBe(false);
    expect(spins(renderToStaticMarkup(<StateMark look={taskLook({ status: "done", depends_on: [] })} />))).toBe(false);
    expect(spins(renderToStaticMarkup(<StateMark look={checkLook({ status: "in_progress", conclusion: null })} />))).toBe(true);
    expect(spins(renderToStaticMarkup(<CheckMark status="doing" />))).toBe(true);
    expect(spins(renderToStaticMarkup(<CheckMark status="todo" />))).toBe(false);
    expect(spins(renderToStaticMarkup(<LiveDot color="#2F6FDB" on />))).toBe(true);
    expect(spins(renderToStaticMarkup(<LiveDot color="#2F6FDB" on={false} />))).toBe(false);
  });

  it("spins the thread mark and status chip only while the thread is in progress", () => {
    const running = thread(1, { status: "running" });
    const queued = thread(2, { status: "queued" });
    expect(spins(renderToStaticMarkup(<ThreadMark thread={running} />))).toBe(true);
    expect(spins(renderToStaticMarkup(<ThreadMark thread={queued} />))).toBe(false);
    const look = threadLook(running);
    expect(spins(renderToStaticMarkup(<StatusPill tone={look.tone} label={look.label} spin={look.spin} />))).toBe(true);
    expect(threadLook(thread(3, { status: "done" })).spin).toBeFalsy();
  });

  it("rotates with CSS and only breathes with reduced motion", () => {
    const css = readFileSync(new URL("../index.css", import.meta.url), "utf8");
    expect(css).toMatch(/\.at-spin\s*{\s*animation:\s*at-spin/);
    expect(css).toMatch(/prefers-reduced-motion: reduce\)\s*{\s*\.at-spin\s*{\s*animation:\s*at-breathe/);
  });
});

describe("approval settings", () => {
  it("offers the lead's time to decide in minutes", () => {
    expect(LEAD_TIMEOUTS).toContain(120);
    expect(leadTimeoutLabel(120)).toBe("2 分钟");
    expect(leadTimeoutLabel(45)).toBe("45 秒");
  });
});
