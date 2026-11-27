// Memory files: a project's (pid) or my own (pid null, every project reads it). memorySource is
// the one place that picks the endpoints for either; the helpers are pure.
import { projects, userMemory } from "@/api/actions";
import { keys, useMemory, useMemoryFile, useUserMemory, useUserMemoryFile } from "@/api/queries";
import type { components } from "@/api/schema";
import type { MemoryBrief } from "@/api/views";

export type MemoryBody = components["schemas"]["MemoryBody"];

/** Queries, writes and the keys a write invalidates, for a project's memory or mine. */
export function memorySource(pid: string | null) {
  if (pid === null) {
    return {
      useList: useUserMemory,
      useFile: (name: () => string) => useUserMemoryFile(name),
      put: (name: string, body: MemoryBody) => userMemory.put(name, body),
      remove: (name: string) => userMemory.remove(name),
      invalidate: () => [keys.userMemory],
    };
  }
  return {
    useList: () => useMemory(() => pid),
    useFile: (name: () => string) => useMemoryFile(() => pid, name),
    put: (name: string, body: MemoryBody) => projects.putMemory(pid, name, body),
    remove: (name: string) => projects.removeMemory(pid, name),
    invalidate: () => [keys.memory(pid), keys.overview(pid)],
  };
}

export const MEMORY_INDEX = "MEMORY.md";

/** MEMORY.md (when it exists) and the topic files, as the 记忆 section shows them. */
export function splitMemory(list: MemoryBrief[] | null | undefined): { index?: MemoryBrief; files: MemoryBrief[] } {
  const all = list ?? [];
  return { index: all.find((f) => f.name === MEMORY_INDEX), files: all.filter((f) => f.name !== MEMORY_INDEX) };
}

const WRITERS: Record<string, string> = { user: "你", platform: "平台", coordinator: "统筹" };

/** Who last wrote a memory file: 你 / 平台 / 统筹 / lead-1 · #3. */
export function memoryWriter(by: string): string {
  return WRITERS[by] ?? by;
}

/** A new file's name as the server stores it: trimmed, ".md" added. */
export function memoryFileName(name: string): string {
  const n = name.trim();
  return !n || n.toLowerCase().endsWith(".md") ? n : `${n}.md`;
}
