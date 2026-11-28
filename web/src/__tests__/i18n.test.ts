// Interface language: every locale file matches zh-CN (the generated source), every message
// compiles, and the account choice / browser language pick the right language.
import { execFileSync } from "node:child_process";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createI18n } from "vue-i18n";
import zh from "@/i18n/locales/zh-CN.json";
import en from "@/i18n/locales/en.json";
import ko from "@/i18n/locales/ko.json";
import ja from "@/i18n/locales/ja.json";
import { applyChoice, currentLocale, detectLocale, resolveLocale, setLocale, syncAccountLocale } from "@/i18n";
import { sessionText } from "@/components/app/session/session";

const LOCALES: Record<string, Record<string, string>> = { en, ko, ja };
const params = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("locale files", () => {
  it("zh-CN.json is in sync with the $t calls and no locale misses a key", () => {
    const script = path.resolve(__dirname, "../../scripts/i18n-extract.mjs");
    expect(() => execFileSync(process.execPath, [script, "--check"], { stdio: "pipe" })).not.toThrow();
  });

  it.each(Object.keys(LOCALES))("%s has every key with the same placeholders", (lang) => {
    const msgs = LOCALES[lang];
    for (const [key] of Object.entries(zh)) {
      expect(msgs[key], `${lang}: ${key}`).toBeTypeOf("string");
      expect(params(msgs[key]), `${lang}: ${key}`).toEqual(params(key));
    }
  });

  it("every message compiles and renders without warnings", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const i18n = createI18n({
      legacy: false,
      locale: "zh-CN",
      messages: { "zh-CN": zh, en, ko, ja },
      warnHtmlMessage: false,
      messageResolver: (obj, p) => ((obj as Record<string, unknown>)[p] as string | undefined) ?? null,
    });
    for (const lang of ["zh-CN", "en", "ko", "ja"]) {
      i18n.global.locale.value = lang as "zh-CN";
      for (const key of Object.keys(zh)) {
        const values = Object.fromEntries(params(key).map((p) => [p, `<${p}>`]));
        const out = i18n.global.t(key, values);
        for (const p of params(key)) expect(out, `${lang}: ${key}`).toContain(`<${p}>`);
      }
    }
    expect(warn).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
    warn.mockRestore();
    error.mockRestore();
  });
});

describe("choosing the language", () => {
  afterEach(() => setLocale("zh-CN"));

  it("detects the browser language", () => {
    expect(detectLocale(["zh-TW", "en"])).toBe("zh-CN");
    expect(detectLocale(["ko-KR"])).toBe("ko");
    expect(detectLocale(["ja"])).toBe("ja");
    expect(detectLocale(["fr-FR", "en-US"])).toBe("en");
    expect(detectLocale(["de"])).toBe("en");
    expect(detectLocale([])).toBe("zh-CN");
  });

  it("an explicit choice wins over the browser", () => {
    expect(resolveLocale("ja")).toBe("ja");
    expect(resolveLocale("xx")).toBe(detectLocale());
  });

  it("reloads only when the account language differs from the page's", async () => {
    await setLocale("en");
    const reload = vi.fn();
    expect(syncAccountLocale("en", reload)).toBe(false);
    expect(reload).not.toHaveBeenCalled();
    expect(syncAccountLocale("ko", reload)).toBe(true);
    expect(reload).toHaveBeenCalledOnce();
    applyChoice("en", reload);
    expect(reload).toHaveBeenCalledOnce();
    applyChoice("ja", reload);
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it("speaks of sessions in each language", async () => {
    await setLocale("zh-CN");
    expect(sessionText("组长在讨论串里")).toBe("员工在会话里");
    await setLocale("en");
    expect(currentLocale()).toBe("en");
    expect(sessionText("The lead opened two threads")).toBe("The employee opened two sessions");
    await setLocale("ja");
    expect(sessionText("リーダーがスレッドを開く")).toBe("スタッフがセッションを開く");
  });
});
