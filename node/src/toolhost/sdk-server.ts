import { createSdkMcpServer, tool, type McpSdkServerConfigWithInstance } from "@anthropic-ai/claude-agent-sdk";
import type { PlatformToolSpec } from "../protocol.js";
import type { McpToolResult } from "./broker.js";
import { zodShape } from "./schema.js";

export type ToolInvoker = (tool: string, args: Record<string, unknown>) => Promise<McpToolResult>;

export const PLATFORM_SERVER = "platform";
export const PLATFORM_PREFIX = `mcp__${PLATFORM_SERVER}__`;
const DAY_MS = 86_400_000;

/** In-process SDK MCP server "platform" for one Claude session (architecture.md §6.3). */
export function platformSdkServer(specs: PlatformToolSpec[], invoke: ToolInvoker): McpSdkServerConfigWithInstance {
  const tools = specs.map((spec) =>
    tool(spec.name, spec.description, zodShape(spec.input_schema), (args) => invoke(spec.name, args as Record<string, unknown>), { alwaysLoad: true }),
  );
  return createSdkMcpServer({ name: PLATFORM_SERVER, version: "2.0.0", tools, alwaysLoad: true, timeout: DAY_MS });
}
