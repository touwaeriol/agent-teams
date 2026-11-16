import { describe, expect, it } from "vitest";
import { fenceCjkUrls } from "@/lib/autolink";

describe("fenceCjkUrls", () => {
  it("ends a bare URL before CJK text", () => {
    expect(fenceCjkUrls("PR 是 http://localgh/local/hello/pull/1。组长没有审查")).toBe("PR 是 <http://localgh/local/hello/pull/1>。组长没有审查");
    expect(fenceCjkUrls("见https://a.b/c，然后")).toBe("见<https://a.b/c>，然后");
  });
  it("leaves URLs followed by ASCII, links, angle links and code alone", () => {
    for (const s of ["see https://a.b/c now", "[pr](https://a.b/c)。", "<https://a.b/c>。", "`https://a.b/c。`", "```\nhttps://a.b/c。\n```"]) {
      expect(fenceCjkUrls(s)).toBe(s);
    }
  });
});
