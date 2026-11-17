<script setup lang="ts">
/**
 * Mic button beside send: click (or hold, with 按住录音) to talk; the chevron opens the settings.
 * While active it is a blue mic on a light-blue circle and a click always ends it: cancels while
 * connecting (a spinner ring), stops while recording, stops waiting while the last words settle.
 */
import { ChevronDown, Mic, MicOff } from "lucide-vue-next";
import { computed, ref } from "vue";
import { cn } from "@/lib/utils";
import type { VoiceInput } from "./useVoiceInput";
import VoiceMenu from "./VoiceMenu.vue";

const props = withDefaults(defineProps<{ v: VoiceInput; size?: number }>(), { size: 32 });
const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const shortcut = isMac ? "⌥M" : "Alt+M";
const phase = computed(() => props.v.state.phase);
const active = computed(() => phase.value === "connecting" || phase.value === "recording" || phase.value === "stopping");
const label = computed(() => {
  if (phase.value === "connecting") return "正在连接…点击取消";
  if (phase.value === "stopping") return "正在整理文字…点击立即结束";
  if (phase.value === "recording") return props.v.prefs.pushToTalk ? "松开结束，或点击停止" : "点击停止（Esc）";
  return props.v.prefs.pushToTalk ? `按住说话（${shortcut}）` : `语音输入（${shortcut}）`;
});
/** A soft halo that follows the microphone level while recording. */
const halo = computed(() => (phase.value === "recording" ? `0 0 0 ${Math.round(Math.min(1, props.v.state.level * 2.5) * 4)}px rgb(47 111 219 / 0.18)` : "none"));

/** Remounts the menu after it closes, so it opens on the main view again. */
const menuKey = ref(0);
/** The click that follows a push-to-talk press is part of that press, not a toggle. */
let pressed = false;

function click() {
  if (pressed) {
    pressed = false;
    return;
  }
  if (active.value || !props.v.prefs.pushToTalk) props.v.toggle();
}
function down(e: PointerEvent) {
  if (!props.v.prefs.pushToTalk || e.button !== 0 || active.value) return;
  pressed = true;
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  props.v.press();
}
const up = () => props.v.prefs.pushToTalk && props.v.release();
</script>

<template>
  <span class="flex shrink-0 items-center gap-0.5" data-testid="voice">
    <button
      type="button"
      :aria-label="label"
      :title="phase === 'error' ? v.state.error : label"
      :aria-pressed="active"
      :data-phase="phase"
      :class="
        cn(
          'relative flex shrink-0 select-none items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-raised hover:text-foreground',
          active && 'bg-[#2F6FDB]/10 text-[#2F6FDB] hover:bg-[#2F6FDB]/20 hover:text-[#2F6FDB]',
          phase === 'error' && 'text-destructive',
        )
      "
      :style="{ width: `${size}px`, height: `${size}px`, touchAction: 'none', boxShadow: halo }"
      data-testid="voice-mic"
      @click="click"
      @pointerdown="down"
      @pointerup="up"
      @pointercancel="up"
      @contextmenu.prevent
    >
      <span
        v-if="phase === 'connecting' || phase === 'stopping'"
        class="absolute inset-[3px] animate-spin rounded-full border-2 border-[#2F6FDB]/25 border-t-[#2F6FDB]"
        aria-hidden="true"
        data-testid="voice-spinner"
      />
      <MicOff v-if="phase === 'error'" class="size-[18px]" :stroke-width="1.8" />
      <Mic v-else class="size-[18px]" :stroke-width="active ? 2.1 : 1.8" />
    </button>
    <el-popover trigger="click" placement="top-end" :width="300" :show-arrow="false" popper-class="!rounded-xl !p-1.5" @after-leave="menuKey++">
      <template #reference>
        <button
          type="button"
          aria-label="语音设置"
          title="麦克风、语言、按住录音"
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
