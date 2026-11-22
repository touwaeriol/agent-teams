// Session moves between nodes (protocol.md §4.11). session.export packs a closed session's runtime
// transcript and a snapshot of each checkout and uploads it; session.open.import downloads it on
// the target node after the checkouts are prepared, rewrites the source node's paths to this
// node's, restores the checkouts and lets the driver resume the same conversation.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import type { SessionExport, SessionExported, SessionOpen } from "../protocol.js";
import type { Logger } from "../util/log.js";
import { isInside, resolveInRoot } from "../util/paths.js";
import { findTranscript } from "../sessions/claude/transcript.js";
import { ArchiveError, checkName, pack, unpack, type Entry } from "./archive.js";
import { conversationDigest } from "./digest.js";
import { restoreRepo, snapshotRepo } from "./snapshot.js";
import { claudeProjectDirName, findRollout, listFiles, rewritePaths } from "./transcripts.js";
import type { MoveTransfer } from "./transfer.js";

export interface MoverDeps {
  root: string;
  claudeConfigDir: string;
  codexHome: string;
  /** Scratch space (the daemon's state dir). */
  tmpDir: string;
  /** Platform hooks dir: daemon git in employee-writable repositories runs hardened (git/safe.ts). */
  hooksDir: string;
  transfer: MoveTransfer;
  log: Logger;
}

/** manifest.json of an archive. */
export interface MoveManifest {
  v: 1;
  runtime: "claude" | "codex";
  sdk_session_id?: string;
  codex_thread_id?: string;
  /** The source node's absolute paths, rewritten to this node's on import. */
  root: string;
  claude_config_dir: string;
  codex_home: string;
  /** Codex: the rollout's path under CODEX_HOME. */
  codex_rel?: string;
  repos: Array<{ cwd: string; head: string; snapshot: string; bundle: string }>;
}

const MANIFEST = "manifest.json";
const CLAUDE_FILE = "claude/session.jsonl";
const CLAUDE_DIR = "claude/session/";
const CODEX_FILE = "codex/rollout.jsonl";

export class Mover {
  constructor(private readonly d: MoverDeps) {}

  /** session.export: never throws; a failure is session.exported {ok:false, error}. */
  async export(b: SessionExport): Promise<SessionExported> {
    try {
      const archive = await this.pack(b);
      const sha256 = createHash("sha256").update(archive).digest("hex");
      await this.d.transfer.upload(b.export_id, archive, sha256);
      this.d.log.info({ session: b.session_id, export: b.export_id, bytes: archive.length }, "session exported");
      return { export_id: b.export_id, session_id: b.session_id, ok: true, size: archive.length, sha256 };
    } catch (err) {
      this.d.log.warn({ session: b.session_id, export: b.export_id, err: (err as Error).message }, "session export failed");
      return { export_id: b.export_id, session_id: b.session_id, ok: false, error: (err as Error).message.slice(0, 2000) };
    }
  }

  /**
   * The live handoff digest of a session's conversation (digest.ts), paths relative to its cwd;
   * "" when the transcript is not there (yet).
   */
  digestOf(runtime: "claude" | "codex", resume: { sdk_session_id?: string | null; codex_thread_id?: string | null } | undefined, absCwd: string): string {
    const file = runtime === "claude"
      ? resume?.sdk_session_id && findTranscript(this.d.claudeConfigDir, resume.sdk_session_id)
      : resume?.codex_thread_id && findRollout(this.d.codexHome, resume.codex_thread_id);
    if (!file) return "";
    const text = rewritePaths(readFileSync(file, "utf8"), [[`${resolve(absCwd)}/`, ""], [`${resolve(this.d.root)}/`, ""]]);
    return conversationDigest(runtime, text);
  }

  private async pack(b: SessionExport): Promise<Buffer> {
    const manifest: MoveManifest = { v: 1, runtime: b.runtime, root: resolve(this.d.root), claude_config_dir: resolve(this.d.claudeConfigDir), codex_home: resolve(this.d.codexHome), repos: [] };
    const entries: Entry[] = [];
    if (b.runtime === "claude") this.packClaude(b, manifest, entries);
    else this.packCodex(b, manifest, entries);
    for (const [i, rel] of (b.repos ?? []).entries()) {
      const cwd = resolveInRoot(this.d.root, rel);
      if (!existsSync(join(cwd, ".git"))) continue; // never checked out here: the target prepares it from origin
      const snap = await snapshotRepo(cwd, `refs/agent-teams/move/${b.export_id}/${i}`, this.d.hooksDir, this.d.tmpDir);
      const name = `git/${i}.bundle`;
      entries.push({ name, data: snap.bundle });
      manifest.repos.push({ cwd: rel, head: snap.head, snapshot: snap.snapshot, bundle: name });
    }
    entries.unshift({ name: MANIFEST, data: Buffer.from(JSON.stringify(manifest)) });
    return pack(entries);
  }

  private packClaude(b: SessionExport, m: MoveManifest, entries: Entry[]): void {
    const id = b.resume.sdk_session_id;
    if (!id) throw new Error("no sdk_session_id to export");
    const file = findTranscript(this.d.claudeConfigDir, id);
    if (!file) throw new Error(`transcript ${id} not found on this node`);
    m.sdk_session_id = id;
    entries.push({ name: CLAUDE_FILE, data: readFileSync(file) });
    // Subagent transcripts and large tool results live in <id>/ next to the file.
    const dir = file.replace(/\.jsonl$/, "");
    for (const rel of listFiles(dir)) entries.push({ name: CLAUDE_DIR + rel, data: readFileSync(join(dir, rel)) });
  }

  private packCodex(b: SessionExport, m: MoveManifest, entries: Entry[]): void {
    const id = b.resume.codex_thread_id;
    if (!id) throw new Error("no codex_thread_id to export");
    const file = findRollout(this.d.codexHome, id);
    if (!file) throw new Error(`rollout of thread ${id} not found on this node`);
    m.codex_thread_id = id;
    m.codex_rel = file.slice(resolve(this.d.codexHome).length + 1).split("\\").join("/");
    entries.push({ name: CODEX_FILE, data: readFileSync(file) });
  }

  /**
   * session.open.import, after the checkouts were prepared: throws when anything does not fit
   * (the session then answers resume_failed and the console hands off with a recap).
   */
  async import(spec: SessionOpen, absCwd: string): Promise<void> {
    const imp = spec.import!;
    const archive = await this.d.transfer.download(imp.export_id);
    if (archive.length !== imp.size) throw new ArchiveError(`archive is ${archive.length} bytes, expected ${imp.size}`);
    if (createHash("sha256").update(archive).digest("hex") !== imp.sha256) throw new ArchiveError("archive sha256 does not match");
    const entries = new Map(unpack(archive).map((e) => [e.name, e.data]));
    const m = manifestOf(entries.get(MANIFEST), spec);
    await this.restoreRepos(m, entries);
    const pairs: Array<[string, string]> = [[m.root, resolve(this.d.root)], [m.claude_config_dir, resolve(this.d.claudeConfigDir)], [m.codex_home, resolve(this.d.codexHome)]];
    if (m.runtime === "claude") this.placeClaude(m, entries, absCwd, pairs);
    else this.placeCodex(m, entries, pairs);
    this.d.log.info({ session: spec.session_id, export: imp.export_id, repos: m.repos.length }, "session imported");
  }

  private async restoreRepos(m: MoveManifest, entries: Map<string, Buffer>): Promise<void> {
    if (m.repos.length === 0) return;
    const scratch = mkdtempSync(join(this.d.tmpDir, "at-import-"));
    try {
      for (const r of m.repos) {
        const cwd = resolveInRoot(this.d.root, r.cwd);
        if (!existsSync(join(cwd, ".git"))) throw new Error(`checkout ${r.cwd} was not prepared on this node`);
        const data = entries.get(r.bundle);
        if (!data) throw new ArchiveError(`archive has no ${r.bundle}`);
        const file = join(scratch, basename(r.bundle));
        writeFileSync(file, data, { mode: 0o600 });
        await restoreRepo(cwd, file, r, this.d.hooksDir);
      }
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  }

  /** The transcript goes where the CLI looks for this cwd: <config>/projects/<cwd as a dir name>/<id>.jsonl. */
  private placeClaude(m: MoveManifest, entries: Map<string, Buffer>, absCwd: string, pairs: Array<[string, string]>): void {
    const main = entries.get(CLAUDE_FILE);
    if (!main) throw new ArchiveError(`archive has no ${CLAUDE_FILE}`);
    const dir = join(this.d.claudeConfigDir, "projects", claudeProjectDirName(absCwd));
    const id = m.sdk_session_id!;
    writeText(join(dir, `${id}.jsonl`), main, pairs);
    for (const [name, data] of entries) {
      if (!name.startsWith(CLAUDE_DIR)) continue;
      const target = join(dir, id, name.slice(CLAUDE_DIR.length));
      if (!isInside(join(dir, id), target)) throw new ArchiveError(`bad entry ${name}`);
      if (name.endsWith(".jsonl")) writeText(target, data, pairs);
      else writeFile(target, data);
    }
  }

  private placeCodex(m: MoveManifest, entries: Map<string, Buffer>, pairs: Array<[string, string]>): void {
    const data = entries.get(CODEX_FILE);
    if (!data) throw new ArchiveError(`archive has no ${CODEX_FILE}`);
    const rel = m.codex_rel?.startsWith("sessions/") ? m.codex_rel : `sessions/${basename(m.codex_rel ?? `rollout-moved-${m.codex_thread_id}.jsonl`)}`;
    checkName(rel);
    writeText(join(this.d.codexHome, rel), data, pairs);
  }
}

/** The archive's manifest, checked against the session it is imported into. */
function manifestOf(raw: Buffer | undefined, spec: SessionOpen): MoveManifest {
  if (!raw) throw new ArchiveError("archive has no manifest");
  const m = JSON.parse(raw.toString("utf8")) as MoveManifest;
  if (m.v !== 1) throw new ArchiveError(`archive version ${String(m.v)} is not supported`);
  if (m.runtime !== spec.runtime) throw new ArchiveError(`archive is a ${m.runtime} session, not ${spec.runtime}`);
  const want = spec.runtime === "claude" ? spec.resume?.sdk_session_id : spec.resume?.codex_thread_id;
  const got = m.runtime === "claude" ? m.sdk_session_id : m.codex_thread_id;
  if (!want || want !== got) throw new ArchiveError(`archive holds conversation ${got ?? "?"}, not ${want ?? "?"}`);
  if (m.runtime === "claude" && !/^[A-Za-z0-9-]{1,100}$/.test(got)) throw new ArchiveError("bad sdk_session_id");
  if (!Array.isArray(m.repos)) throw new ArchiveError("bad manifest");
  return m;
}

function writeText(path: string, data: Buffer, pairs: Array<[string, string]>): void {
  writeFile(path, Buffer.from(rewritePaths(data.toString("utf8"), pairs), "utf8"));
}

function writeFile(path: string, data: Buffer): void {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, data, { mode: 0o600 });
}
