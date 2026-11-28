// Interface language (web i18n). Simplified Chinese is the source language and always bundled;
// the others load on demand before the app mounts. The language is fixed for a page load: a
// change saves the choice and reloads, so strings computed once at module load (option lists,
// labels in plain .ts files) are always in the current language.
import { createI18n } from "vue-i18n";
import zhCN from "./locales/zh-CN.json";

export const LOCALES = ["zh-CN", "en", "ko", "ja"] as const;
export type Locale = (typeof LOCALES)[number];

/** Each language in its own script (the picker shows these, never translated). */
export const LOCALE_NAMES: Record<Locale, string> = { "zh-CN": "简体中文", en: "English", ko: "한국어", ja: "日本語" };

export type Messages = typeof zhCN;

declare module "vue-i18n" {
  // Typed keys: t("no.such.key") fails the type check.
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  export interface DefineLocaleMessage extends Messages {}
}

// Other languages may lag behind zh-CN: a missing key falls back to the Chinese text.
const loaders: Record<Exclude<Locale, "zh-CN">, () => Promise<{ default: Record<string, string> }>> = {
  en: () => import("./locales/en.json"),
  ko: () => import("./locales/ko.json"),
  ja: () => import("./locales/ja.json"),
};

const STORAGE_KEY = "locale";

export const isLocale = (v: unknown): v is Locale => typeof v === "string" && (LOCALES as readonly string[]).includes(v);

/** The best shipped language for the browser's preferences (zh-TW and other Chinese → zh-CN, else English). */
export function detectLocale(langs: readonly string[] = typeof navigator === "undefined" ? [] : navigator.languages ?? [navigator.language]): Locale {
  for (const raw of langs) {
    const base = raw.toLowerCase().split("-")[0];
    if (base === "zh") return "zh-CN";
    if (base === "en" || base === "ko" || base === "ja") return base;
  }
  return langs.length ? "en" : "zh-CN";
}

/** The account's saved choice cached in this browser ("" = follow the browser). */
export function storedChoice(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

function storeChoice(choice: string) {
  try {
    if (choice) localStorage.setItem(STORAGE_KEY, choice);
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* private mode: the account setting still applies after sign-in */
  }
}

/** The language for a choice: the chosen one, or the browser's. */
export const resolveLocale = (choice: string): Locale => (isLocale(choice) ? choice : detectLocale());

const i18n = createI18n({
  legacy: false,
  locale: "zh-CN",
  fallbackLocale: "zh-CN",
  messages: { "zh-CN": zhCN } as Record<Locale, Messages>,
  missingWarn: false,
  fallbackWarn: false,
  // Messages are plain text rendered with {{ }} / text bindings (escaped), never v-html; "<container name>" is not HTML.
  warnHtmlMessage: false,
  // Flat messages: the key is the whole Chinese sentence, dots and all.
  messageResolver: (obj, path) => ((obj as Record<string, unknown>)[path] as string | undefined) ?? null,
});

export default i18n;

/**
 * Translate: $t("简体中文原文", { name }). Keys are the Simplified Chinese text itself (gettext
 * style); zh-CN.json is generated from the calls (npm run i18n:extract). Named $t so it never
 * clashes with the many local `t` variables (threads, tasks) and works the same in templates.
 */
export const $t = i18n.global.t;

/** A message from the server: { key, params } (server/internal/msg). */
export interface ServerText {
  key: string;
  params?: Record<string, string> | null;
  /** Params that are messages themselves; params holds their Chinese as a fallback. */
  nested?: Record<string, ServerText> | null;
}

/**
 * Shows a server message in the interface language. Keys are the server's Chinese text with
 * {placeholders}; a key this build does not know (a newer server, or free text such as an
 * upstream error) is shown as rendered Chinese, never compiled as a message.
 */
export function serverText(m: ServerText | null | undefined, fallback = ""): string {
  if (!m?.key) return fallback;
  const params: Record<string, string> = { ...(m.params ?? {}) };
  for (const [k, inner] of Object.entries(m.nested ?? {})) params[k] = serverText(inner, params[k] ?? "");
  if (i18n.global.te(m.key, "zh-CN")) return $t(m.key as keyof Messages, params);
  return Object.entries(params).reduce((s, [k, v]) => s.split(`{${k}}`).join(v), m.key);
}

/** The current language. */
export const currentLocale = (): Locale => i18n.global.locale.value as Locale;

/** Load a language's messages and switch to it (before mount). */
export async function setLocale(locale: Locale) {
  if (locale !== "zh-CN" && !i18n.global.availableLocales.includes(locale)) {
    i18n.global.setLocaleMessage(locale, (await loaders[locale]()).default as Messages);
  }
  i18n.global.locale.value = locale;
  if (typeof document !== "undefined") document.documentElement.lang = locale;
}

/** Start-up: the cached account choice, else the browser's language. */
export const initLocale = () => setLocale(resolveLocale(storedChoice()));

/**
 * After /api/me: remember the account's choice and reload once if it means another language
 * than the one this page started with. Returns true when a reload was started.
 */
export function syncAccountLocale(choice: string, reload = () => window.location.reload()): boolean {
  storeChoice(choice);
  if (resolveLocale(choice) === currentLocale()) return false;
  reload();
  return true;
}

/** The user picked a language in settings (already saved on the account). */
export function applyChoice(choice: string, reload = () => window.location.reload()) {
  storeChoice(choice);
  if (resolveLocale(choice) !== currentLocale()) reload();
}

/** BCP 47 tag for Intl formatting. */
export const intlLocale = () => currentLocale();
