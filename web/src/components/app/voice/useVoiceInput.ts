// Voice input for one composer: wires a VoiceController to the composer's text and input element.
// Shown only when the admin enabled voice (GET /api/voice/config).
import { computed, nextTick, onBeforeUnmount, reactive, ref, shallowRef, type Ref } from "vue";
import { useMe } from "@/api/queries";
import { useVoiceConfig, voice as voiceApi } from "@/api/voice";
import { toast } from "@/lib/toast";
import { openMic, startMeter } from "@/lib/voice/audio";
import { VoiceController, type VoiceDeps, type VoiceState } from "@/lib/voice/controller";
import { connectOpenAI } from "@/lib/voice/openai";
import { readPrefs, writePrefs, type LanguageOption, type VoicePrefs } from "@/lib/voice/prefs";
import type { Part, Transcript } from "@/lib/voice/transcript";

type Field = HTMLInputElement | HTMLTextAreaElement;

export interface VoiceInput {
  enabled: Readonly<Ref<boolean>>;
  languages: Readonly<Ref<LanguageOption[]>>;
  state: VoiceState;
  prefs: VoicePrefs;
  /** Text with grey partial runs while speech is being recognised (null: nothing pending). */
  parts: Readonly<Ref<Part[] | null>>;
  active: Readonly<Ref<boolean>>;
  setPrefs(patch: Partial<VoicePrefs>): void;
  toggle(): void;
  press(): void;
  release(): void;
  /** Stops and waits for the last transcripts (before sending). */
  finish(): Promise<void>;
  cancel(): void;
  /** Alt+M: toggles, or press/release in push-to-talk mode. Returns true when handled. */
  onKeyDown(e: KeyboardEvent): boolean;
  onKeyUp(e: KeyboardEvent): void;
}

export const isVoiceKey = (e: KeyboardEvent) => e.altKey && !e.ctrlKey && !e.metaKey && e.code === "KeyM";

/** Real browser dependencies; tests build a VoiceController with fakes instead. */
export function browserDeps(onText: (t: Transcript) => void): VoiceDeps {
  return {
    mint: (language) => voiceApi.session(language),
    openMic,
    connect: connectOpenAI,
    meter: startMeter,
    now: () => Date.now(),
    onText,
    onNotice: (m) => toast.warning(m),
  };
}

export function useVoiceInput(text: Ref<string>, field: Ref<Field | null>, makeDeps = browserDeps): VoiceInput {
  const config = useVoiceConfig();
  const me = useMe();
  const userId = computed(() => me.data.value?.id ?? "");
  const prefs = reactive<VoicePrefs>(readPrefs(userId.value));
  let loadedFor = userId.value;
  const state = reactive<VoiceState>({ phase: "idle", level: 0, error: "" });
  const parts = shallowRef<Part[] | null>(null);
  const enabled = computed(() => Boolean(config.data.value?.enabled));
  const languages = computed<LanguageOption[]>(() => config.data.value?.languages ?? []);
  const holding = ref(false);

  function render(t: Transcript) {
    text.value = t.value;
    parts.value = t.pending ? t.parts : null;
    const caret = t.caret;
    void nextTick(() => {
      const el = field.value;
      if (!el) return;
      el.setSelectionRange(caret, caret);
    });
  }

  const ctl = new VoiceController(state, makeDeps(render));
  const active = computed(() => state.phase === "connecting" || state.phase === "recording" || state.phase === "stopping");

  function syncPrefs() {
    if (loadedFor === userId.value) return;
    loadedFor = userId.value;
    Object.assign(prefs, readPrefs(loadedFor));
  }

  function setPrefs(patch: Partial<VoicePrefs>) {
    syncPrefs();
    Object.assign(prefs, patch);
    writePrefs(userId.value, { ...prefs });
  }

  function start() {
    syncPrefs();
    ctl.reset();
    const el = field.value;
    const value = text.value;
    const selStart = el?.selectionStart ?? value.length;
    const selEnd = el?.selectionEnd ?? selStart;
    el?.focus();
    const language = languages.value.some((l) => l.code === prefs.language) ? prefs.language : "auto";
    void ctl.start({ text: value, selStart, selEnd, language, deviceId: prefs.deviceId }).then(() => {
      if (state.phase === "error") toast.error(state.error);
      // Released before the connection was up: stop now.
      if (prefs.pushToTalk && !holding.value && state.phase === "recording") void ctl.stop();
    });
  }

  function toggle() {
    if (active.value) void ctl.stop();
    else start();
  }

  function press() {
    holding.value = true;
    if (!active.value) start();
  }

  function release() {
    if (!holding.value) return;
    holding.value = false;
    if (state.phase === "recording") void ctl.stop();
  }

  function onKeyDown(e: KeyboardEvent): boolean {
    if (active.value && e.key === "Escape") {
      e.preventDefault();
      void ctl.stop();
      return true;
    }
    if (!enabled.value || !isVoiceKey(e)) return false;
    e.preventDefault();
    if (e.repeat) return true;
    if (prefs.pushToTalk) press();
    else toggle();
    return true;
  }

  function onKeyUp(e: KeyboardEvent) {
    if (prefs.pushToTalk && holding.value && (e.code === "KeyM" || e.key === "Alt")) release();
  }

  onBeforeUnmount(() => ctl.active && ctl.cancel());

  return {
    enabled,
    languages,
    state,
    prefs,
    parts,
    active,
    setPrefs,
    toggle,
    press,
    release,
    finish: () => ctl.stop(),
    cancel: () => ctl.cancel(),
    onKeyDown,
    onKeyUp,
  };
}
