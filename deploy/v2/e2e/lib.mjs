// Shared helpers for the local end-to-end scripts: env file, a cookie/CSRF-aware API client,
// SSE reading and small assertions. Node >= 22 (global fetch, WebSocket).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const here = dirname(fileURLToPath(import.meta.url));
export const repoRoot = join(here, "..", "..", "..");

export function readEnv(file = join(here, "..", ".env.e2e")) {
  const env = {};
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const m = line.match(/^([A-Z0-9_]+)=(.*)$/);
    if (m) env[m[1]] = m[2];
  }
  return env;
}

export function check(cond, message, detail) {
  if (!cond) throw new Error(`FAIL ${message}${detail === undefined ? "" : `: ${JSON.stringify(detail).slice(0, 2000)}`}`);
  console.log(`ok   ${message}`);
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** waitFor polls fn until it returns a truthy value or the timeout passes. */
export async function waitFor(label, fn, { timeoutMs = 120_000, everyMs = 2000 } = {}) {
  const until = Date.now() + timeoutMs;
  let last;
  while (Date.now() < until) {
    last = await fn().catch((e) => (console.log(`  .. ${label}: ${e.message}`), undefined));
    if (last) return last;
    await sleep(everyMs);
  }
  throw new Error(`timeout waiting for ${label}`);
}

/** Client keeps the session cookie and sends X-CSRF-Token on writes, like the web app. */
export class Client {
  constructor(base, cookies = {}) {
    this.base = base;
    this.cookies = { ...cookies };
  }

  cookieHeader() {
    return Object.entries(this.cookies).map(([k, v]) => `${k}=${v}`).join("; ");
  }

  keepCookies(res) {
    for (const raw of res.headers.getSetCookie?.() ?? []) {
      const [pair] = raw.split(";");
      const i = pair.indexOf("=");
      this.cookies[pair.slice(0, i)] = pair.slice(i + 1);
    }
  }

  async raw(method, path, body, headers = {}) {
    const h = { Cookie: this.cookieHeader(), ...headers };
    if (body !== undefined) h["Content-Type"] = "application/json";
    if (method !== "GET" && this.cookies.agent_teams_csrf) h["X-CSRF-Token"] = this.cookies.agent_teams_csrf;
    const res = await fetch(this.base + path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body), redirect: "manual" });
    this.keepCookies(res);
    const text = await res.text();
    let json;
    try { json = text ? JSON.parse(text) : null; } catch { json = text; }
    return { status: res.status, json, headers: res.headers };
  }

  async call(method, path, body) {
    const r = await this.raw(method, path, body);
    if (r.status >= 400) throw new Error(`${method} ${path} → ${r.status} ${JSON.stringify(r.json)}`);
    return r.json;
  }

  get(path) { return this.call("GET", path); }
  post(path, body = {}) { return this.call("POST", path, body); }
  put(path, body) { return this.call("PUT", path, body); }
  patch(path, body) { return this.call("PATCH", path, body); }
}

/** SSE collects events from an event-stream endpoint into an array until close(). */
export class SSE {
  constructor(client, path) {
    this.events = [];
    this.ctrl = new AbortController();
    this.done = this.read(client, path).catch((e) => { if (e.name !== "AbortError") console.log(`sse ${path}: ${e.message}`); });
  }

  async read(client, path) {
    const res = await fetch(client.base + path, { headers: { Cookie: client.cookieHeader(), Accept: "text/event-stream" }, signal: this.ctrl.signal });
    if (res.status !== 200) throw new Error(`SSE ${path} → ${res.status}`);
    const decoder = new TextDecoder();
    let buf = "";
    for await (const chunk of res.body) {
      buf += decoder.decode(chunk, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        this.push(buf.slice(0, i));
        buf = buf.slice(i + 2);
      }
    }
  }

  push(block) {
    const ev = { event: "message", data: "" };
    for (const line of block.split("\n")) {
      const m = line.match(/^(\w+): ?(.*)$/);
      if (m && m[1] === "data") ev.data += m[2];
      else if (m) ev[m[1]] = m[2];
    }
    if (!ev.data) return;
    try { ev.json = JSON.parse(ev.data); } catch { /* keep text */ }
    this.events.push(ev);
  }

  types() { return [...new Set(this.events.map((e) => e.event))]; }
  close() { this.ctrl.abort(); }
}
