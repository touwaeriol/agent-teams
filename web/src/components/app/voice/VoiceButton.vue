<script setup lang="ts">
/** Mic button beside send: click (or hold, with 按住录音) to talk; the chevron opens the settings. */
import { ChevronDown, Mic, MicOff } from "lucide-vue-next";
import { computed } from "vue";
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import type { VoiceInput } from "./useVoiceInput";
import VoiceMenu from "./VoiceMenu.vue";

const props = withDefaults(defineProps<{ v: VoiceInput; size?: number }>(), { size: 32 });
const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const shortcut = isMac ? "⌥M" : "Alt+M";
const phase = computed(() => props.v.state.phase);
const recording = computed(() => phase.value === "recording" || phase.value === "stopping");
const label = computed(() => {
  if (phase.value === "connecting") return "正在连接语音服务";
  if (recording.value) return props.v.prefs.pushToTalk ? "松开结束录音" : "结束录音";
  return props.v.prefs.pushToTalk ? `按住说话（${shortcut}）` : `语音输入（${shortcut}）`;
});
/** Five bars following the microphone level. */
const bars = computed(() => [0.5, 0.8, 1, 0.8, 0.5].map((k) => Math.max(0.18, Math.min(1, props.v.state.level * 2.2 * k))));

function click() {
  if (!props.v.prefs.pushToTalk) props.v.toggle();
}
function down(e: PointerEvent) {
  if (!props.v.prefs.pushToTalk || e.button !== 0) return;
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  props.v.press();
}
const up = () => props.v.prefs.pushToTalk && props.v.release();
</script>

<template>
  <span :class="cn('flex shrink-0 items-center rounded-lg', recording && 'bg-[#2F6FDB]/10')" data-testid="voice">
    <button
      type="button"
      :aria-label="label"
      :title="phase === 'error' ? v.state.error : label"
      :aria-pressed="recording"
      :class="
        cn(
          'flex shrink-0 select-none items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground',
          recording && 'text-[#2F6FDB] hover:bg-transparent hover:text-[#2F6FDB]',
          phase === 'error' && 'text-destructive',
        )
      "
      :style="{ width: `${size}px`, height: `${size}px`, touchAction: 'none' }"
      data-testid="voice-mic"
      @click="click"
      @pointerdown="down"
      @pointerup="up"
      @pointercancel="up"
      @contextmenu.prevent
    >
      <Spinner v-if="phase === 'connecting'" class="size-4" />
      <span v-else-if="recording" class="flex h-4 items-center gap-[2px]" aria-hidden="true" data-testid="voice-level">
        <span v-for="(h, i) in bars" :key="i" class="w-[3px] rounded-full bg-current transition-[height] duration-75" :style="{ height: `${Math.round(h * 16)}px` }" />
      </span>
      <MicOff v-else-if="phase === 'error'" class="size-[18px]" :stroke-width="1.8" />
      <Mic v-else class="size-[18px]" :stroke-width="1.8" />
    </button>
    <el-popover trigger="click" placement="top-end" :width="284" :show-arrow="false" popper-class="!p-2">
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
      <VoiceMenu :prefs="v.prefs" :languages="v.languages.value" :shortcut="shortcut" @change="v.setPrefs" />
    </el-popover>
  </span>
</template>
