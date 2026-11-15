import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

/** Path of an SDK session transcript under <config>/projects/<encoded cwd>/<id>.jsonl, if any. */
export function findTranscript(configDir: string, sdkSessionId: string): string | null {
  const projects = join(configDir, "projects");
  if (!existsSync(projects)) return null;
  for (const dir of readdirSync(projects)) {
    const p = join(projects, dir, `${sdkSessionId}.jsonl`);
    if (existsSync(p)) return p;
  }
  return null;
}

/** uuids of user messages recorded in a transcript. */
export function transcriptUserUuids(path: string): Set<string> {
  const out = new Set<string>();
  for (const line of readFileSync(path, "utf8").split("\n")) {
    const uuid = userUuid(line);
    if (uuid) out.add(uuid);
  }
  return out;
}

function userUuid(line: string): string | null {
  if (!line.includes('"user"')) return null;
  try {
    const rec = JSON.parse(line) as { type?: string; uuid?: string };
    return rec.type === "user" && typeof rec.uuid === "string" ? rec.uuid : null;
  } catch {
    return null;
  }
}
