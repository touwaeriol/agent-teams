import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Inbox } from "../src/link/inbox.js";
import { Outbox } from "../src/link/outbox.js";
import { Dispatcher } from "../src/link/dispatcher.js";
import { silentLogger } from "../src/util/log.js";
import type { Frame } from "../src/protocol.js";

let dir: string;
beforeEach(() => (dir = mkdtempSync(join(tmpdir(), "at-journal-"))));
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("outbox", () => {
  it("continues seq after a restart and keeps unacked frames", () => {
    const a = new Outbox(dir);
    a.append("session.event", { n: 1 });
    a.append("session.event", { n: 2 });
    a.append("session.event", { n: 3 });
    a.ack(1);
    const b = new Outbox(dir);
    expect(b.epoch).toBe(a.epoch);
    expect(b.lastSentSeq).toBe(3);
    expect(b.unacked().map((f) => f.seq)).toEqual([2, 3]);
    expect(b.append("x", {}).seq).toBe(4);
  });

  it("compacts once everything is acknowledged", () => {
    const a = new Outbox(dir);
    a.append("a", {});
    a.append("b", {});
    a.ack(2);
    const b = new Outbox(dir);
    expect(b.unacked()).toEqual([]);
    expect(b.lastSentSeq).toBe(2);
  });

  it("replaces orphaned frames with substitutes", () => {
    const a = new Outbox(dir);
    a.append("approval.request", { approval_id: "ap_1", session_id: "s" });
    a.append("session.event", {});
    a.rewritePending((f) => (f.type === "approval.request" ? [{ type: "approval.cancel", body: { approval_id: "ap_1" } }] : null));
    const b = new Outbox(dir);
    expect(b.unacked().map((f) => [f.type, f.seq])).toEqual([["session.event", 2], ["approval.cancel", 3]]);
  });

  it("starts a new epoch when the state directory is wiped", () => {
    const a = new Outbox(dir);
    rmSync(join(dir, "outbox"), { recursive: true });
    expect(new Outbox(dir).epoch).not.toBe(a.epoch);
  });
});

describe("dispatcher + inbox", () => {
  function frame(type: string, seq: number, body: Record<string, unknown>): Frame {
    return { v: 2, type, id: `c_${seq}`, seq, ts: new Date().toISOString(), body };
  }

  it("runs each command once, re-sends replies for repeats, keeps lanes ordered", async () => {
    const sent: Array<[string, object]> = [];
    const acks: number[] = [];
    const link = { send: (t: string, b: object) => sent.push([t, b]), ackCommand: (s: number) => acks.push(s), sendError: () => undefined };
    const order: string[] = [];
    let runs = 0;
    const handlers = {
      "session.send": {
        lane: (b: Record<string, unknown>) => `s:${b.session_id}`,
        run: async (b: Record<string, unknown>) => {
          runs++;
          order.push(`start ${b.msg_id}`);
          await new Promise((r) => setTimeout(r, b.msg_id === "m1" ? 30 : 1));
          order.push(`end ${b.msg_id}`);
          return [{ type: "session.delivered", body: { session_id: b.session_id, msg_id: b.msg_id } }];
        },
      },
    };
    const inbox = new Inbox(dir);
    const d = new Dispatcher(link, inbox, handlers, silentLogger);
    d.handle(frame("session.send", 1, { session_id: "s", msg_id: "m1", text: "a" }));
    d.handle(frame("session.send", 2, { session_id: "s", msg_id: "m2", text: "b" }));
    // Wait for both runs and their acks rather than a fixed delay: a loaded CI runner can take longer,
    // and a run still finishing after afterEach removed the directory would write into a missing journal.
    await vi.waitFor(() => expect(sent.filter(([t]) => t === "session.delivered").length).toBe(2), { timeout: 5000, interval: 5 });
    expect(order).toEqual(["start m1", "end m1", "start m2", "end m2"]);
    d.handle(frame("session.send", 3, { session_id: "s", msg_id: "m1", text: "a" }));
    expect(runs).toBe(2);
    expect(sent.filter(([t]) => t === "session.delivered").length).toBe(3);
    expect(acks).toEqual([1, 2, 3]);
    const again = new Inbox(dir);
    expect(again.acked).toBe(3);
    expect(again.repliesFor("session.send:m2")).toEqual([{ type: "session.delivered", body: { session_id: "s", msg_id: "m2" } }]);
  });

  it("re-runs unfinished git jobs after a restart but forgets session commands", async () => {
    const inbox = new Inbox(dir);
    inbox.accept(frame("git.prepare", 5, { job_id: "g_1" }), "git.prepare:g_1");
    inbox.accept(frame("session.send", 6, { msg_id: "m_1" }), "session.send:m_1");
    const ran: string[] = [];
    const link = { send: () => undefined, ackCommand: () => undefined, sendError: () => undefined };
    const run = async (_b: Record<string, unknown>, f: Frame) => (ran.push(f.type), []);
    const d = new Dispatcher(link, new Inbox(dir), { "git.prepare": { run }, "session.send": { run } }, silentLogger);
    d.resumeUnfinished();
    await vi.waitFor(() => expect(new Inbox(dir).unfinished()).toEqual([]), { timeout: 5000, interval: 5 });
    expect(ran).toEqual(["git.prepare"]);
  });
});
