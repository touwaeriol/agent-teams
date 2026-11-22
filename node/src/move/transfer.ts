// Session-move archives travel over HTTP with the node token (PUT / GET /nodes/moves/{id}), through
// the same proxy as the console link: they are too large for the WebSocket's 4 MiB frames.
import { request as httpRequest, type IncomingMessage } from "node:http";
import { request as httpsRequest } from "node:https";
import { proxyAgentFor } from "../link/proxy.js";
import { consoleBase } from "../sessions/images.js";
import { sleep } from "../util/async.js";
import { MAX_ARCHIVE_BYTES } from "./archive.js";

export interface MoveTransfer {
  upload(exportId: string, data: Buffer, sha256: string): Promise<void>;
  download(exportId: string): Promise<Buffer>;
}

const TIMEOUT_MS = 10 * 60_000;
const RETRY_MS = [2_000, 10_000];

export function consoleTransfer(wsUrl: string, token: string): MoveTransfer {
  const base = consoleBase(wsUrl);
  const url = (id: string) => `${base}/nodes/moves/${encodeURIComponent(id)}`;
  return {
    upload: (id, data, sha256) => retry(async () => {
      const res = await send("PUT", url(id), token, data, { "Content-Type": "application/octet-stream", "X-Content-SHA256": sha256 });
      if (res.status !== 204 && res.status !== 200) throw new Error(`upload: HTTP ${res.status} ${res.body.toString("utf8").slice(0, 200)}`);
    }),
    download: (id) => retry(async () => {
      const res = await send("GET", url(id), token);
      if (res.status !== 200) throw new Error(`download: HTTP ${res.status} ${res.body.toString("utf8").slice(0, 200)}`);
      return res.body;
    }),
  };
}

async function retry<T>(fn: () => Promise<T>): Promise<T> {
  for (let i = 0; ; i++) {
    try {
      return await fn();
    } catch (err) {
      if (i >= RETRY_MS.length || /HTTP 4\d\d/.test((err as Error).message)) throw err;
      await sleep(RETRY_MS[i]!);
    }
  }
}

function send(method: string, target: string, token: string, body?: Buffer, headers: Record<string, string> = {}): Promise<{ status: number; body: Buffer }> {
  const u = new URL(target);
  const req = (u.protocol === "https:" ? httpsRequest : httpRequest)(u, {
    method, agent: proxyAgentFor(target), timeout: TIMEOUT_MS,
    headers: { Authorization: `Bearer ${token}`, ...headers, ...(body ? { "Content-Length": String(body.length) } : {}) },
  });
  return new Promise((resolve, reject) => {
    req.on("timeout", () => req.destroy(new Error(`${method} ${u.pathname} timed out`)));
    req.on("error", reject);
    req.on("response", (res: IncomingMessage) => {
      const chunks: Buffer[] = [];
      let size = 0;
      res.on("data", (c: Buffer) => {
        size += c.length;
        if (size > MAX_ARCHIVE_BYTES + 1024) res.destroy(new Error("response too large"));
        else chunks.push(c);
      });
      res.on("error", reject);
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: Buffer.concat(chunks) }));
    });
    req.end(body);
  });
}
