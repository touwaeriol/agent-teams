import { createHash, createPublicKey, verify } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { NodeUpgrade } from "./protocol.js";
import { run } from "./git/exec.js";

export interface UpgradeOptions {
  consoleHttpBase: string;
  installDir: string;
  releaseKeyPath: string;
  fetchFn?: typeof fetch;
}

export class UpgradeError extends Error {}

const VERSION_RE = /^\d+\.\d+\.\d+$/;

/**
 * node.upgrade (protocol.md §9): download <console>/node/dist/agent-teams-node-<v>.tgz,
 * check sha256 and the Ed25519 release signature, unpack to node-<v>/, install
 * dependencies, switch the `current` symlink. The caller exits with 75 so systemd
 * restarts the new version.
 */
export async function applyUpgrade(msg: NodeUpgrade, o: UpgradeOptions): Promise<void> {
  if (!VERSION_RE.test(msg.version)) throw new UpgradeError("invalid version");
  if (!existsSync(o.releaseKeyPath)) throw new UpgradeError("no release key installed");
  const tgz = await download(`${o.consoleHttpBase}/node/dist/agent-teams-node-${msg.version}.tgz`, o.fetchFn ?? fetch);
  verifyRelease(tgz, msg, readFileSync(o.releaseKeyPath, "utf8"));
  await unpack(tgz, msg.version, o.installDir);
}

async function download(url: string, fetchFn: typeof fetch): Promise<Buffer> {
  const res = await fetchFn(url);
  if (!res.ok) throw new UpgradeError(`download failed: HTTP ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

export function verifyRelease(tgz: Buffer, msg: NodeUpgrade, publicKey: string): void {
  const sum = createHash("sha256").update(tgz).digest("hex");
  if (sum !== msg.sha256.toLowerCase()) throw new UpgradeError("sha256 mismatch");
  const key = createPublicKey(publicKey.includes("BEGIN") ? publicKey : { key: Buffer.from(publicKey.trim(), "base64"), format: "der", type: "spki" });
  if (!verify(null, tgz, key, Buffer.from(msg.signature, "base64"))) throw new UpgradeError("bad signature");
}

async function unpack(tgz: Buffer, version: string, installDir: string): Promise<void> {
  const target = join(installDir, `node-${version}`);
  const staging = `${target}.staging`;
  rmSync(staging, { recursive: true, force: true });
  mkdirSync(staging, { recursive: true });
  const file = join(staging, "package.tgz");
  writeFileSync(file, tgz);
  await must(run("tar", ["-xzf", file, "-C", staging, "--strip-components=1"]), "unpack failed");
  rmSync(file);
  await must(run("npm", ["ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund"], { cwd: staging, timeoutMs: 15 * 60_000 }), "npm ci failed");
  rmSync(target, { recursive: true, force: true });
  renameSync(staging, target);
  const link = join(installDir, "current");
  const tmp = `${link}.new`;
  rmSync(tmp, { force: true });
  symlinkSync(`node-${version}`, tmp);
  renameSync(tmp, link);
}

async function must(p: Promise<{ code: number; stderr: string }>, what: string): Promise<void> {
  const r = await p;
  if (r.code !== 0) throw new UpgradeError(`${what}: ${r.stderr.slice(-500)}`);
}
