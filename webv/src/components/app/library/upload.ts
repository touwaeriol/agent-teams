// Skill and plugin uploads: a zip (sent as base64) or a picked folder of text files.
import type { SkillFile } from "@/api/views";

/** Text files of a picked folder: hidden files and folders (.git, .DS_Store) are left out. */
export function pickedSkillFiles(entries: { path: string; text: string }[]): SkillFile[] {
  return entries
    .filter((e) => !e.path.split("/").some((part) => part.startsWith(".") || part === "__MACOSX"))
    .map((e) => ({ path: e.path, content: e.text }));
}

/** Base64 of bytes (zip upload), in chunks so large files do not overflow the call stack. */
export function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

export async function zipOf(f: Blob): Promise<string> {
  return toBase64(new Uint8Array(await f.arrayBuffer()));
}

export async function readFolder(list: ArrayLike<File>): Promise<SkillFile[]> {
  const entries = await Promise.all(Array.from(list).map(async (f) => ({ path: f.webkitRelativePath || f.name, text: await f.text() })));
  return pickedSkillFiles(entries);
}

export const PLUGIN_HELP =
  "插件是 Claude Code 插件包：根目录 .claude-plugin/plugin.json，加上 skills/、commands/、agents/、hooks/hooks.json 或 .mcp.json。打包成 zip 上传（最多 200 个文件、合计 1 MB），同名再次上传会成为新版本。插件的工具调用照常需要审批（技能和命令里的 allowed-tools 会被去掉）；hooks 会在节点上直接运行命令，不经审批。";
