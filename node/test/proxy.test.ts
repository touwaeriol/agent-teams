import { describe, expect, it } from "vitest";
import { HttpsProxyAgent } from "https-proxy-agent";
import { bypassProxy, proxyAgentFor } from "../src/link/proxy.js";

describe("console link proxy", () => {
  const P = "http://u:p@proxy.example:3128";

  it("uses HTTPS_PROXY for wss and HTTP_PROXY for ws, either case", () => {
    const a = proxyAgentFor("wss://agent.example.com/nodes/connect", { HTTPS_PROXY: P });
    expect(a).toBeInstanceOf(HttpsProxyAgent);
    expect((a as HttpsProxyAgent<string>).proxy.host).toBe("proxy.example:3128");
    expect(proxyAgentFor("wss://agent.example.com/x", { http_proxy: P })).toBeInstanceOf(HttpsProxyAgent);
    expect(proxyAgentFor("ws://agent.example.com/x", { HTTPS_PROXY: P })).toBeUndefined();
    expect(proxyAgentFor("ws://agent.example.com/x", { HTTP_PROXY: P })).toBeInstanceOf(HttpsProxyAgent);
  });

  it("goes direct without a proxy variable", () => {
    expect(proxyAgentFor("wss://agent.example.com/x", {})).toBeUndefined();
  });

  it("honors NO_PROXY", () => {
    const u = (s: string) => new URL(s);
    const env = { NO_PROXY: "localhost, 127.0.0.1,.internal,example.org:8443" };
    expect(bypassProxy(u("ws://localhost:9000/x"), env)).toBe(true);
    expect(bypassProxy(u("ws://127.0.0.1:9000/x"), env)).toBe(true);
    expect(bypassProxy(u("wss://a.b.internal/x"), env)).toBe(true);
    expect(bypassProxy(u("wss://sub.example.org:8443/x"), env)).toBe(true);
    expect(bypassProxy(u("wss://example.org/x"), env)).toBe(false);
    expect(bypassProxy(u("wss://agent.example.com/x"), env)).toBe(false);
    expect(bypassProxy(u("wss://x/y"), { no_proxy: "*" })).toBe(true);
    expect(proxyAgentFor("ws://127.0.0.1:1/x", { HTTP_PROXY: P, no_proxy: "127.0.0.1" })).toBeUndefined();
  });
});
