import { createServer, type IncomingMessage, type Server as HttpServer, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { timingSafeEqual } from "node:crypto";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import type { PlatformToolSpec } from "../protocol.js";
import { randomSecret } from "../util/ids.js";
import type { Logger } from "../util/log.js";
import type { ToolInvoker } from "./sdk-server.js";

interface Route {
  secret: string;
  tools: PlatformToolSpec[];
  invoke: ToolInvoker;
}

/**
 * Loopback streamable-HTTP MCP server for Codex sessions (architecture.md §6.3). Only
 * listens on 127.0.0.1; each session is served at /mcp/<session_id>/<random secret>,
 * and every rotate() (thread/start, thread/resume) invalidates the previous path.
 */
export class LoopbackToolHost {
  private server: HttpServer | null = null;
  private port = 0;
  private readonly routes = new Map<string, Route>();

  constructor(private readonly log: Logger) {}

  async start(): Promise<void> {
    if (this.server) return;
    const server = createServer((req, res) => void this.onRequest(req, res));
    server.requestTimeout = 0;
    server.headersTimeout = 60_000;
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    this.port = (server.address() as AddressInfo).port;
    this.server = server;
  }

  async stop(): Promise<void> {
    const s = this.server;
    this.server = null;
    if (!s) return;
    s.closeAllConnections();
    await new Promise<void>((resolve) => s.close(() => resolve()));
  }

  /** Registers (or re-keys) a session and returns its new URL. */
  rotate(sessionId: string, tools: PlatformToolSpec[], invoke: ToolInvoker): string {
    const secret = randomSecret();
    this.routes.set(sessionId, { secret, tools, invoke });
    return `http://127.0.0.1:${this.port}/mcp/${encodeURIComponent(sessionId)}/${secret}`;
  }

  remove(sessionId: string): void {
    this.routes.delete(sessionId);
  }

  private async onRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const route = this.match(req.url ?? "");
    if (!route) return void res.writeHead(404).end();
    try {
      await this.serve(route, req, res);
    } catch (err) {
      this.log.warn({ err: (err as Error).message }, "loopback MCP request failed");
      if (!res.headersSent) res.writeHead(500).end();
    }
  }

  private match(url: string): Route | null {
    const m = /^\/mcp\/([^/]+)\/([^/?#]+)/.exec(url);
    if (!m) return null;
    const route = this.routes.get(decodeURIComponent(m[1]!));
    if (!route || !safeEqual(route.secret, m[2]!)) return null;
    return route;
  }

  private async serve(route: Route, req: IncomingMessage, res: ServerResponse): Promise<void> {
    const server = buildMcpServer(route);
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    res.on("close", () => {
      void transport.close();
      void server.close();
    });
    await server.connect(transport);
    await transport.handleRequest(req, res);
  }
}

function buildMcpServer(route: Route): Server {
  const server = new Server({ name: "platform", version: "2.0.0" }, { capabilities: { tools: {} } });
  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: route.tools.map((t) => ({ name: t.name, description: t.description, inputSchema: t.input_schema as { type: "object" } })),
  }));
  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    const known = route.tools.some((t) => t.name === req.params.name);
    if (!known) return { content: [{ type: "text", text: `unknown tool ${req.params.name}` }], isError: true };
    return route.invoke(req.params.name, (req.params.arguments ?? {}) as Record<string, unknown>);
  });
  return server;
}

function safeEqual(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}
