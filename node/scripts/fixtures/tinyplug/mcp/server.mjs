// Minimal stdio MCP server: one tool "fruit" that returns BANANA-3.
import { createInterface } from "node:readline";
const send = (m) => process.stdout.write(JSON.stringify(m) + "\n");
const tools = [{ name: "fruit", description: "Returns the MCP code word", inputSchema: { type: "object", properties: {} } }];
const handlers = {
  initialize: (p) => ({ protocolVersion: p?.protocolVersion ?? "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "calc", version: "0.1.0" } }),
  "tools/list": () => ({ tools }),
  "tools/call": () => ({ content: [{ type: "text", text: "BANANA-3" }] }),
  ping: () => ({}),
};
createInterface({ input: process.stdin }).on("line", (line) => {
  let m; try { m = JSON.parse(line); } catch { return; }
  if (m.id === undefined) return;
  const h = handlers[m.method];
  send(h ? { jsonrpc: "2.0", id: m.id, result: h(m.params) } : { jsonrpc: "2.0", id: m.id, error: { code: -32601, message: "no method" } });
});
