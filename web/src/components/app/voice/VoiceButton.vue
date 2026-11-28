<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * Mic button beside send, in one of two modes (VoicePrefs.pushToTalk, the 按住录音 switch):
 * - click mode (default): click to start, click again to stop. While connecting a click cancels,
 *   while the last words settle a click stops waiting.
 * - push-to-talk: press to start, let go to stop. Moving off the button while held keeps recording
 *   until the release (pointer capture, plus window-level pointerup/pointercancel and blur/hidden
 *   in useVoiceInput as backstops, so a lost release can't leave it recording). A click that only
 *   ends a press does nothing more; a bare keyboard click stops a recording but never starts one.
 * While the microphone is open a live level wave shows on the left; recording is a solid blue
 * button, connecting/stopping a light one with a spinner ring.
 */
import { ChevronDown, Mic, MicOff } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref } from "vue";
import { cn } from "@/lib/utils";
import type { VoiceInput } from "./useVoiceInput";
import VoiceMenu from "./VoiceMenu.vue";
import VoiceWave from "./VoiceWave.vue";

const props = withDefaults(defineProps<{ v: VoiceInput; size?: number }>(), { size: 32 });
const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const shortcut = isMac ? "⌥M" : "Alt+M";
const phase = computed(() => props.v.state.phase);
const ptt = computed(() => props.v.prefs.pushToTalk);
const active = computed(() => phase.value === "connecting" || phase.value === "recording" || phase.value === "stopping");
const label = computed(() => {
  if (phase.value === "connecting") return ptt.value ? $t("正在连接…连上后开始录音，松开结束") : $t("正在连接…点击取消");
  if (phase.value === "stopping") return ptt.value ? $t("正在整理文字…") : $t("正在整理文字…点击立即结束");
  if (phase.value === "recording") return ptt.value ? $t("正在录音，松开结束") : $t("正在录音，点击结束（Esc）");
  return ptt.value ? $t("按住说话，松开结束（{shortcut}）", { shortcut }) : $t("语音输入：点击开始，再点击结束（{shortcut}）", { shortcut });
});
const wave = computed(() => phase.value === "connecting" || phase.value === "recording");
/** The inline composer is short on room: fewer, shorter bars. */
const small = computed(() => props.size < 32);

/** Remounts the menu after it closes, so it opens on the main view again. */
const menuKey = ref(0);

/** Click mode: a second click this soon after a toggle is a double click, not "stop"/"stop waiting". */
const DOUBLE_CLICK_MS = 350;
let lastToggle = -Infinity;
/** The pointer that went down: a touch doesn't focus the text field (no soft keyboard). */
let pointerType = "";
/** Push-to-talk: the pointer currently holding the button. */
let holder: number | null = null;
/** Push-to-talk: the click that follows a press (if the browser sends one) belongs to it. */
let swallowClick = false;

function click(e: MouseEvent) {
  if (ptt.value) {
    if (swallowClick || e.detail > 0) return; // part of a press (or a pointer click without a press)
    if (phase.value === "connecting" || phase.value === "recording") props.v.toggle(); // keyboard: stop only
    return;
  }
  const now = performance.now();
  if (now - lastToggle < DOUBLE_CLICK_MS) return;
  lastToggle = now;
  props.v.toggle(pointerType !== "touch" && pointerType !== "pen");
  pointerType = "";
}

function down(e: PointerEvent) {
  pointerType = e.pointerType ?? "";
  if (!ptt.value || (e.button ?? 0) !== 0) return;
  // A hold whose end we never saw, already released by useVoiceInput (blur, hidden): forget it.
  if (holder !== null && !props.v.holding.value) endHold();
  if (holder !== null) return; // a second finger is ignored
  e.preventDefault(); // no text selection or focus change from the press itself
  holder = e.pointerId ?? 0;
  swallowClick = true;
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  listen(true);
  props.v.press(pointerType !== "touch" && pointerType !== "pen");
}

function up(e: PointerEvent) {
  if (holder === null || (e.pointerId ?? 0) !== holder) return;
  endHold(false);
  // The click (if any) is dispatched right after this pointerup, before the timer runs.
  setTimeout(() => (swallowClick = false));
}

/**
 * Ends the press however it ends: released, cancelled, capture lost, the window losing focus or
 * the tab hidden. Clears every trace of it so the next press starts fresh.
 */
function endHold(lost: unknown = true) {
  if (holder === null) return;
  holder = null;
  if (lost !== false) swallowClick = false; // no click follows a press that ended this way
  listen(false);
  props.v.release();
}
const onHidden = () => document.visibilityState === "hidden" && endHold();

function listen(on: boolean) {
  if (on) {
    window.addEventListener("pointerup", up, true);
    window.addEventListener("pointercancel", up, true);
    window.addEventListener("blur", endHold);
    document.addEventListener("visibilitychange", onHidden);
  } else {
    window.removeEventListener("pointerup", up, true);
    window.removeEventListener("pointercancel", up, true);
    window.removeEventListener("blur", endHold);
    document.removeEventListener("visibilitychange", onHidden);
  }
}

onBeforeUnmount(() => listen(false));
</script>

<template>
  <span class="flex shrink-0 items-center gap-0.5" data-testid="voice">
    <VoiceWave v-if="wave" :level="v.state.level" :bars="small ? 7 : 12" :max="small ? 14 : 18" :class="small ? 'mr-0.5' : 'mr-1.5'" />
    <button
      type="button"
      :aria-label="label"
      :title="phase === 'error' ? v.state.error : label"
      :aria-pressed="active"
      :data-phase="phase"
      :data-mode="ptt ? 'hold' : 'click'"
      :class="
        cn(
          'relative flex shrink-0 select-none items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-raised hover:text-foreground',
          (phase === 'connecting' || phase === 'stopping') && 'bg-[#2F6FDB]/10 text-[#2F6FDB] hover:bg-[#2F6FDB]/20 hover:text-[#2F6FDB]',
          phase === 'recording' && 'bg-[#2F6FDB] text-white shadow-[0_0_0_3px_rgb(47_111_219/0.18)] hover:bg-[#2860C4] hover:text-white',
          phase === 'error' && 'text-destructive',
        )
      "
      :style="{ width: `${size}px`, height: `${size}px`, touchAction: 'none', WebkitTouchCallout: 'none', WebkitUserSelect: 'none' }"
      data-testid="voice-mic"
      @click="click"
      @pointerdown="down"
      @pointerup="up"
      @pointercancel="up"
      @lostpointercapture="endHold"
      @contextmenu.prevent
    >
      <span
        v-if="phase === 'recording'"
        class="pointer-events-none absolute inset-0 rounded-full bg-[#2F6FDB]/40 motion-safe:animate-ping motion-reduce:hidden"
        style="animation-duration: 1.6s"
        aria-hidden="true"
        data-testid="voice-pulse"
      />
      <span
        v-if="phase === 'connecting' || phase === 'stopping'"
        class="absolute inset-[3px] animate-spin rounded-full border-2 border-[#2F6FDB]/25 border-t-[#2F6FDB]"
        aria-hidden="true"
        data-testid="voice-spinner"
      />
      <MicOff v-if="phase === 'error'" class="size-[18px]" :stroke-width="1.8" />
      <Mic v-else class="relative size-[18px]" :stroke-width="active ? 2.1 : 1.8" />
    </button>
    <el-popover trigger="click" placement="top-end" :width="300" :show-arrow="false" popper-class="!rounded-xl !p-1.5" @after-leave="menuKey++">
      <template #reference>
        <button
          type="button"
          :aria-label="$t('语音设置')"
          :title="$t('麦克风、语言、按住录音')"
          class="-ml-1 flex h-6 w-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-raised hover:text-foreground"
          data-testid="voice-settings"
        >
          <ChevronDown class="size-3.5" :stroke-width="2" />
        </button>
      </template>
      <VoiceMenu :key="menuKey" :prefs="v.prefs" :languages="v.languages.value" :shortcut="shortcut" @change="v.setPrefs" />
    </el-popover>
  </span>
</template>
