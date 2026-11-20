// Per-user voice preferences in this browser: microphone, language, push-to-talk.
import { readLocal, writeLocal } from "@/lib/persist";

export interface VoicePrefs {
  /** "" = the system default microphone. */
  deviceId: string;
  /** "auto" or an ISO-639-1 code from /api/voice/config. */
  language: string;
  pushToTalk: boolean;
}

/** Click mode (click to start, click again to stop) unless the user turns 按住录音 on. */
export const DEFAULT_PREFS: VoicePrefs = { deviceId: "", language: "auto", pushToTalk: false };

/**
 * v2 key. The v1 key (voice-prefs:<user>) is read once to keep the microphone and language, but
 * not 按住录音: that switch used to flip from a click anywhere on its row, so many v1 entries
 * have it on without the user meaning to. Everyone starts (again) in click mode.
 */
export const prefsKey = (userId: string) => `voice-prefs:v2:${userId || "me"}`;
export const legacyPrefsKey = (userId: string) => `voice-prefs:${userId || "me"}`;

function parse(raw: string | null): VoicePrefs | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<VoicePrefs> | null;
    if (!v || typeof v !== "object") return null;
    return {
      deviceId: typeof v.deviceId === "string" ? v.deviceId : "",
      language: typeof v.language === "string" && v.language ? v.language : "auto",
      // Only a real true turns push-to-talk on; anything else ("true", 1, missing) is click mode.
      pushToTalk: v.pushToTalk === true,
    };
  } catch {
    return null;
  }
}

export function readPrefs(userId: string): VoicePrefs {
  const current = parse(readLocal(prefsKey(userId)));
  if (current) return current;
  const legacy = parse(readLocal(legacyPrefsKey(userId)));
  if (!legacy) return { ...DEFAULT_PREFS };
  const migrated = { ...legacy, pushToTalk: false };
  writeLocal(prefsKey(userId), JSON.stringify(migrated));
  writeLocal(legacyPrefsKey(userId), null);
  return migrated;
}

/** Listeners told when prefs are written (every composer on the page follows the same prefs). */
const listeners = new Set<() => void>();

export function writePrefs(userId: string, prefs: VoicePrefs): void {
  writeLocal(prefsKey(userId), JSON.stringify({ deviceId: prefs.deviceId, language: prefs.language, pushToTalk: prefs.pushToTalk === true }));
  listeners.forEach((f) => f());
}

/** Calls f whenever prefs change: written on this page or in another tab. Returns the unsubscribe. */
export function onPrefsChange(f: () => void): () => void {
  listeners.add(f);
  const onStorage = (e: StorageEvent) => {
    if (e.key === null || e.key.startsWith("voice-prefs:")) f();
  };
  if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(f);
    if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
  };
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
