<script setup lang="ts">
// Replay controls: play/pause, 1× / 4× / 16× and a time scrubber.
import { $t } from "@/i18n";
import type { CSSProperties } from "vue";
import { REPLAY_SPEEDS, type ReplayState } from "./replay";

defineProps<{ replay: ReplayState }>();

function clock(ms: number): string {
  const d = new Date(ms);
  const two = (n: number) => String(n).padStart(2, "0");
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

const btn = (on: boolean): CSSProperties => ({
  height: "26px",
  padding: "0 9px",
  borderRadius: "7px",
  border: on ? "none" : "1px solid #4A4D59",
  cursor: "pointer",
  background: on ? "#F0EDE6" : "transparent",
  color: on ? "#1F1E1C" : "#E6E3DC",
  font: "inherit",
  fontSize: "12px",
  fontWeight: on ? 600 : 400,
});
const mono = "'JetBrains Mono', monospace";
</script>

<template>
  <div :style="{ display: 'flex', flexDirection: 'column', gap: '8px', fontSize: '12px', color: '#D8D4CB' }">
    <div :style="{ display: 'flex', alignItems: 'center', gap: '6px' }">
      <button
        type="button"
        :style="{ ...btn(true), background: '#C8622A', color: '#FFFFFF' }"
        :aria-label="replay.playing.value ? $t('暂停') : $t('播放')"
        @click="replay.playing.value ? replay.pause() : replay.play()"
      >
        {{ replay.playing.value ? $t("❚❚ 暂停") : $t("▶ 播放") }}
      </button>
      <button v-for="s in REPLAY_SPEEDS" :key="s" type="button" :style="btn(replay.speed.value === s)" :aria-pressed="replay.speed.value === s" @click="replay.setSpeed(s)">{{ s }}×</button>
      <span :style="{ marginLeft: 'auto', fontFamily: mono, color: '#A8A49B' }">{{ clock(replay.time.value) }}</span>
    </div>
    <input
      type="range"
      :aria-label="$t('回放进度')"
      :min="replay.start.value"
      :max="replay.end.value"
      step="100"
      :value="replay.time.value"
      :style="{ width: '100%', accentColor: '#C8622A' }"
      @input="replay.seek(Number(($event.target as HTMLInputElement).value))"
    />
    <div :style="{ display: 'flex', justifyContent: 'space-between', fontFamily: mono, color: '#7E8190', fontSize: '11px' }">
      <span>{{ clock(replay.start.value) }}</span>
      <span>{{ clock(replay.end.value) }}</span>
    </div>
  </div>
</template>
