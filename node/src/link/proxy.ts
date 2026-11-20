import type { Agent } from "node:http";
import { HttpsProxyAgent } from "https-proxy-agent";

type Env = Record<string, string | undefined>;

const pick = (env: Env, name: string) => env[name.toLowerCase()] || env[name.toUpperCase()] || "";

/** True when host[:port] matches NO_PROXY ("*", exact host, ".suffix" / "suffix" domain, optional :port). */
export function bypassProxy(url: URL, env: Env = process.env): boolean {
  const list = pick(env, "no_proxy");
  if (!list) return false;
  const host = url.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  const port = url.port || (url.protocol === "wss:" || url.protocol === "https:" ? "443" : "80");
  return list.split(/[\s,]+/).filter(Boolean).some((raw) => {
    const entry = raw.toLowerCase();
    if (entry === "*") return true;
    const m = /^(.*?)(?::(\d+))?$/.exec(entry.replace(/^\[|\]$/g, ""))!;
    const name = m[1].replace(/^\*?\./, "");
    if (m[2] && m[2] !== port) return false;
    return host === name || host.endsWith(`.${name}`);
  });
}

/**
 * Proxy agent for the console WebSocket from HTTPS_PROXY / HTTP_PROXY (either case), honoring NO_PROXY.
 * The ws library ignores these variables (and NODE_USE_ENV_PROXY), so the link would otherwise go direct.
 */
export function proxyAgentFor(target: string, env: Env = process.env): Agent | undefined {
  const url = new URL(target);
  const secure = url.protocol === "wss:" || url.protocol === "https:";
  const proxy = (secure ? pick(env, "https_proxy") : "") || pick(env, "http_proxy");
  if (!proxy || bypassProxy(url, env)) return undefined;
  return new HttpsProxyAgent(proxy);
}

