// Voice input for one composer: wires a VoiceController to the composer's text and input element.
// Shown only when the admin enabled voice (GET /api/voice/config).
import { computed, nextTick, onBeforeUnmount, reactive, ref, shallowRef, watch, type Ref } from "vue";
import { useMe } from "@/api/queries";
import { useVoiceConfig, voice as voiceApi } from "@/api/voice";
import { toast } from "@/lib/toast";
import { openMic, startMeter } from "@/lib/voice/audio";
import { VoiceController, type VoiceDeps, type VoiceState } from "@/lib/voice/controller";
import { connectOpenAI } from "@/lib/voice/openai";
import { connectRelay, relaySession } from "@/lib/voice/relay";
import { onPrefsChange, readPrefs, writePrefs, type LanguageOption, type VoicePrefs } from "@/lib/voice/prefs";
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
  /** Push-to-talk: a press (pointer or Alt+M) is currently held. */
  holding: Readonly<Ref<boolean>>;
  setPrefs(patch: Partial<VoicePrefs>): void;
  /** Click mode: start, or end whatever is going on. focus=false keeps the soft keyboard down (touch). */
  toggle(focus?: boolean): void;
  /** Push-to-talk: held down (starts, or keeps a recording that is still going). */
  press(focus?: boolean): void;
  /** Push-to-talk: let go (stops; a press released while connecting stops once connected). */
  release(): void;
  /** Stops and waits for the last transcripts (before sending). */
  finish(): Promise<void>;
  cancel(): void;
  /** Alt+M: toggles, or press/release in push-to-talk mode. Returns true when handled. */
  onKeyDown(e: KeyboardEvent): boolean;
  onKeyUp(e: KeyboardEvent): void;
}

export const isVoiceKey = (e: KeyboardEvent) => e.altKey && !e.ctrlKey && !e.metaKey && e.code === "KeyM";

/**
 * Real browser dependencies; tests build a VoiceController with fakes instead. transport() is the
 * admin's choice: "relay" (default, audio through our server) or "webrtc" (browser → OpenAI).
 */
export function browserDeps(onText: (t: Transcript) => void, transport: () => string = () => "relay"): VoiceDeps {
  return {
    mint: async (language) => (transport() === "webrtc" ? voiceApi.session(language) : relaySession(language)),
    openMic,
    connect: (session, stream, h) => (session.transport === "webrtc" ? connectOpenAI(session, stream, h) : connectRelay(session, stream, h)),
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

  const ctl = new VoiceController(state, makeDeps(render, () => config.data.value?.transport ?? "relay"));
  const active = computed(() => state.phase === "connecting" || state.phase === "recording" || state.phase === "stopping");

  function syncPrefs() {
    if (loadedFor === userId.value) return;
    loadedFor = userId.value;
    Object.assign(prefs, readPrefs(loadedFor));
  }
  // The button shows and follows the signed-in user's prefs from the start (not only once a
  // recording starts: the mode must not change between the press and the release), and every
  // composer follows a change made in another one (or another tab).
  watch(userId, syncPrefs);
  // Same page: take the written value as is (storage may be unavailable, so reading it back could
  // lose this session's changes). Another tab: read storage again.
  const unsubscribe = onPrefsChange((c) => {
    if (!c) Object.assign(prefs, readPrefs(userId.value));
    else if (c.userId === userId.value) Object.assign(prefs, c.prefs);
  });

  function setPrefs(patch: Partial<VoicePrefs>) {
    syncPrefs();
    writePrefs(userId.value, { ...prefs, ...patch }); // every composer on the page (this one too) takes it
  }

  function start(focus = true) {
    syncPrefs();
    ctl.reset();
    const el = field.value;
    const value = text.value;
    const selStart = el?.selectionStart ?? value.length;
    const selEnd = el?.selectionEnd ?? selStart;
    if (focus) el?.focus();
    const language = languages.value.some((l) => l.code === prefs.language) ? prefs.language : "auto";
    void ctl.start({ text: value, selStart, selEnd, language, deviceId: prefs.deviceId }).then(() => {
      if (state.phase === "error") toast.error(state.error);
      // Released before the connection was up: stop now.
      if (prefs.pushToTalk && !holding.value && state.phase === "recording") void ctl.stop();
    });
  }

  /** Start, or end whatever is going on: cancel while connecting, stop while recording, stop waiting while stopping. */
  function toggle(focus = true) {
    if (state.phase === "stopping") ctl.hurry();
    else if (active.value) void ctl.stop();
    else start(focus);
  }

  // While held, losing the window (alt-tab, a permission prompt taking focus, the tab hidden) ends
  // the press: the key-up or pointer-up that would have ended it will never come.
  const onHidden = () => document.visibilityState === "hidden" && release();
  function watchLoss(on: boolean) {
    if (typeof window === "undefined") return;
    const method = on ? "addEventListener" : "removeEventListener";
    window[method]("blur", release);
    document[method]("visibilitychange", onHidden);
  }

  /**
   * Pressed while the previous recording is still settling its last words: start again once it
   * is done, if still held (rather than dropping the press or cutting the previous text short).
   */
  let queued: boolean | null = null;
  watch(
    () => state.phase,
    (phase) => {
      if (queued === null || phase === "stopping") return;
      const focus = queued;
      queued = null;
      if (holding.value && !active.value) start(focus);
    },
  );

  function press(focus = true) {
    if (!holding.value) watchLoss(true);
    holding.value = true;
    if (state.phase === "stopping") queued = focus;
    else if (!active.value) start(focus);
  }

  function release() {
    if (!holding.value) return;
    holding.value = false;
    queued = null;
    watchLoss(false);
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
    // A hold ends on release whatever the mode is now (the switch may have changed while held).
    if (holding.value && (e.code === "KeyM" || e.key === "Alt")) release();
  }

  onBeforeUnmount(() => {
    unsubscribe();
    if (holding.value) watchLoss(false);
    holding.value = false;
    if (ctl.active) ctl.cancel();
  });

  return {
    enabled,
    languages,
    state,
    prefs,
    parts,
    active,
    holding,
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
