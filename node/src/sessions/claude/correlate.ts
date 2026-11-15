import { sha256Hex } from "../../util/ids.js";

/** Matches platform tool handler calls to the PreToolUse tool_use_id (same tool, same args). */
export class ToolUseCorrelator {
  private readonly waiting = new Map<string, string[]>();

  remember(tool: string, args: unknown, toolUseId: string): void {
    const key = keyOf(tool, args);
    this.waiting.set(key, [...(this.waiting.get(key) ?? []), toolUseId]);
  }

  take(tool: string, args: unknown): string | undefined {
    const key = keyOf(tool, args);
    const list = this.waiting.get(key);
    const id = list?.shift();
    if (list && list.length === 0) this.waiting.delete(key);
    return id;
  }
}

function keyOf(tool: string, args: unknown): string {
  return `${tool}:${sha256Hex(stableJson(args ?? {}))}`;
}

function stableJson(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(stableJson).join(",")}]`;
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return `{${Object.keys(o).sort().map((k) => `${JSON.stringify(k)}:${stableJson(o[k])}`).join(",")}}`;
  }
  return JSON.stringify(v);
}
