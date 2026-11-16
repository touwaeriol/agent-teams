// Per-user voice preferences in this browser: microphone, language, push-to-talk.
import { readLocal, writeLocal } from "@/lib/persist";

export interface VoicePrefs {
  /** "" = the system default microphone. */
  deviceId: string;
  /** "auto" or an ISO-639-1 code from /api/voice/config. */
  language: string;
  pushToTalk: boolean;
}

export const DEFAULT_PREFS: VoicePrefs = { deviceId: "", language: "auto", pushToTalk: false };

export const prefsKey = (userId: string) => `voice-prefs:${userId || "me"}`;

export function readPrefs(userId: string): VoicePrefs {
  const raw = readLocal(prefsKey(userId));
  if (!raw) return { ...DEFAULT_PREFS };
  try {
    const v = JSON.parse(raw) as Partial<VoicePrefs>;
    return {
      deviceId: typeof v.deviceId === "string" ? v.deviceId : "",
      language: typeof v.language === "string" && v.language ? v.language : "auto",
      pushToTalk: v.pushToTalk === true,
    };
  } catch {
    return { ...DEFAULT_PREFS };
  }
}

export function writePrefs(userId: string, prefs: VoicePrefs): void {
  writeLocal(prefsKey(userId), JSON.stringify(prefs));
}

export interface LanguageOption {
  code: string;
  name: string;
  zh: string;
}

/** Language search: code, own name or Chinese name; "" lists all. */
export function filterLanguages(list: LanguageOption[], query: string): LanguageOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return list;
  return list.filter((l) => l.code.toLowerCase().startsWith(q) || l.name.toLowerCase().includes(q) || l.zh.includes(q));
}

export function languageLabel(list: LanguageOption[], code: string): string {
  if (code === "auto") return "自动";
  return list.find((l) => l.code === code)?.name ?? "自动";
}
