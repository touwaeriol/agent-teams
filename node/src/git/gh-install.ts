import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { delimiter, join } from "node:path";

/**
 * The GitHub CLI every employee session gets (architecture.md §10.4). Nodes usually have no
 * `gh`, so the daemon downloads the official release once into <data>/tools/gh-<version>/,
 * checked against both the hashes pinned here and the release's checksums file.
 */
export interface GhRelease {
  version: string;
  /** Release asset per `${platform}-${arch}` with its pinned sha256. */
  assets: Record<string, { name: string; sha256: string }>;
}

export const GH_RELEASE: GhRelease = {
  version: "2.103.3",
  assets: {
    "linux-x64": { name: "gh_2.103.3_linux_amd64.tar.gz", sha256: "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d" },
    "linux-arm64": { name: "gh_2.103.3_linux_arm64.tar.gz", sha256: "ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63" },
    "darwin-x64": { name: "gh_2.103.3_macOS_amd64.zip", sha256: "a2f86418f4350ae6e9ec60d5000bdf7665728436946a97fc27fe50e6ae752682" },
    "darwin-arm64": { name: "gh_2.103.3_macOS_arm64.zip", sha256: "d700207eae9ae98fe2b89535c2c087c07823d4628c56364695a5a07ef311c062" },
  },
};

export const GH_DOWNLOAD_BASE = "https://github.com/cli/cli/releases/download";

export interface GhInstallOptions {
  toolsDir: string;
  release?: GhRelease;
  baseUrl?: string;
  platform?: string;
  arch?: string;
  /** PATH searched for an installed gh first; dirs in `skip` (the platform wrapper) are ignored. */
  path?: string;
  skip?: string[];
  fetchFn?: typeof fetch;
}

/** gh found on PATH, skipping the platform wrapper directory. */
export function ghOnPath(pathVar: string, skip: string[] = []): string | null {
  for (const dir of pathVar.split(delimiter)) {
    if (!dir || skip.includes(dir)) continue;
    const p = join(dir, "gh");
    if (existsSync(p)) return p;
  }
  return null;
}

export function installedGh(toolsDir: string, release: GhRelease = GH_RELEASE): string {
  return join(toolsDir, `gh-${release.version}`, "bin", "gh");
}

/**
 * Resolves a real gh: one already on PATH, else the downloaded copy, else downloads it.
 * Concurrent callers share one download.
 */
export class GhInstaller {
  private pending: Promise<string> | null = null;

  constructor(private readonly o: GhInstallOptions) {}

  resolve(): Promise<string> {
    const local = ghOnPath(this.o.path ?? process.env.PATH ?? "", this.o.skip);
    if (local) return Promise.resolve(local);
    const bin = installedGh(this.o.toolsDir, this.o.release);
    if (existsSync(bin)) return Promise.resolve(bin);
    this.pending ??= downloadGh(this.o).finally(() => (this.pending = null));
    return this.pending;
  }
}

/** Downloads, verifies and unpacks the release; returns the gh binary path. */
export async function downloadGh(o: GhInstallOptions): Promise<string> {
  const release = o.release ?? GH_RELEASE;
  const key = `${o.platform ?? process.platform}-${o.arch ?? process.arch}`;
  const asset = release.assets[key];
  if (!asset) throw new Error(`no gh release for ${key}`);
  const base = `${o.baseUrl ?? GH_DOWNLOAD_BASE}/v${release.version}`;
  const get = o.fetchFn ?? fetch;
  const sums = parseChecksums(await fetchText(get, `${base}/gh_${release.version}_checksums.txt`));
  const data = await fetchBytes(get, `${base}/${asset.name}`);
  const sha = createHash("sha256").update(data).digest("hex");
  if (sha !== asset.sha256 || sums.get(asset.name) !== sha) throw new Error(`gh ${asset.name}: sha256 mismatch`);
  return unpack(o.toolsDir, release.version, asset.name, data);
}

export function parseChecksums(text: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const line of text.split("\n")) {
    const m = /^([0-9a-f]{64})\s+\*?(\S+)$/.exec(line.trim());
    if (m) out.set(m[2]!, m[1]!);
  }
  return out;
}

function unpack(toolsDir: string, version: string, name: string, data: Buffer): string {
  mkdirSync(toolsDir, { recursive: true, mode: 0o755 });
  const stage = mkdtempSync(join(toolsDir, ".gh-"));
  try {
    const archive = join(stage, name);
    writeFileSync(archive, data);
    const out = join(stage, "x");
    mkdirSync(out);
    const r = name.endsWith(".zip")
      ? spawnSync("unzip", ["-q", archive, "-d", out], { encoding: "utf8" })
      : spawnSync("tar", ["-xzf", archive, "-C", out, "--no-same-owner"], { encoding: "utf8" });
    if (r.status !== 0) throw new Error(`cannot unpack ${name}: ${r.stderr || r.error?.message}`);
    const inner = join(out, name.replace(/\.(tar\.gz|zip)$/, ""));
    const bin = join(inner, "bin", "gh");
    if (!existsSync(bin)) throw new Error(`${name} has no bin/gh`);
    chmodSync(bin, 0o755);
    const dest = join(toolsDir, `gh-${version}`);
    if (!existsSync(dest)) renameSync(inner, dest);
    return join(dest, "bin", "gh");
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
}

async function fetchBytes(get: typeof fetch, url: string): Promise<Buffer> {
  const r = await get(url, { redirect: "follow", signal: AbortSignal.timeout(10 * 60_000) });
  if (!r.ok) throw new Error(`GET ${url}: ${r.status}`);
  return Buffer.from(await r.arrayBuffer());
}

async function fetchText(get: typeof fetch, url: string): Promise<string> {
  return (await fetchBytes(get, url)).toString("utf8");
}
