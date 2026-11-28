import { afterEach, describe, expect, it, vi } from "vitest";
import { dockerGuideUrl, enrollModes, enrollText, loadEnrollMode, saveEnrollMode } from "@/pages/settings/nodes/enroll";

const data = { token: "t", install: "curl …", docker: "docker run …", compose: "name: x", guide: "https://backend.example.com/node-docker.md" };

describe("node enrollment modes", () => {
  it("maps each mode to its snippet", () => {
    expect(enrollModes.map((m) => m.id)).toEqual(["host", "docker", "compose"]);
    expect(enrollText(data, "host")).toBe("curl …");
    expect(enrollText(data, "docker")).toBe("docker run …");
    expect(enrollText(data, "compose")).toBe("name: x");
  });

  afterEach(() => vi.unstubAllGlobals());

  it("remembers the last mode and falls back to host", () => {
    expect(loadEnrollMode()).toBe("host"); // no storage at all
    const store = new Map<string, string>([["at.nodes.enrollMode", "bogus"]]);
    vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => store.set(k, v) });
    expect(loadEnrollMode()).toBe("host");
    saveEnrollMode("compose");
    expect(loadEnrollMode()).toBe("compose");
  });

  it("links the public guide on the console origin", () => {
    expect(dockerGuideUrl("https://agent.example.com")).toBe("https://agent.example.com/node-docker.md");
  });
});
