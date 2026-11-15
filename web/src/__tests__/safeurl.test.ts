import { describe, expect, it } from "vitest";
import { embeddableUrl, safeHref } from "../lib/safeurl";

describe("safeHref", () => {
  it("keeps http(s) links", () => {
    expect(safeHref("https://www.figma.com/file/x")).toBe("https://www.figma.com/file/x");
    expect(safeHref("http://intranet.example/doc")).toBe("http://intranet.example/doc");
  });
  it("drops script, data and relative links", () => {
    for (const u of ["javascript:alert(1)", " JavaScript:alert(1)", "data:text/html,x", "/api/me", "", null, undefined]) {
      expect(safeHref(u)).toBeUndefined();
    }
  });
  it("never embeds the app's own origin", () => {
    expect(embeddableUrl("https://app.example/api/me", "https://app.example")).toBeUndefined();
    expect(embeddableUrl("https://www.figma.com/embed?x", "https://app.example")).toBe("https://www.figma.com/embed?x");
  });
});
