<script setup lang="ts">
// Dev-only demo (/dev/office under `npm run dev`): the office driven by simulated status
// events, live or replayed. /office/default.json is imported raw so Vite HMR hot-reloads it.
// A smaller port of web/src/office/demo/OfficeDemo.tsx (no task-board side panel).
import { computed, onBeforeUnmount, ref, shallowRef } from "vue";
import layoutText from "../../../../office/default.json?raw";
import { Office } from "../Office";
import ReplayBar from "../ReplayBar.vue";
import { useReplay } from "../replay";
import type { OfficeLayout } from "../types";
import { useHotLayout } from "../useLayout";
import { DEMO_DATA, DEMO_THREADS, DEMO_TITLES, LOOP_MS, demoRoster, loopEvents, recordedHistory, type DemoEvent } from "./simulation";

const start = Date.now();
const mode = ref<"live" | "replay">("live");
const focus = ref<number | null>(null);
const selected = ref<string | null>(null);
const hot = useHotLayout(JSON.parse(layoutText) as OfficeLayout);
const roster = demoRoster(start);
const live = shallowRef<DemoEvent[]>([]);
const replay = useReplay(recordedHistory(start, 4));
const replayRoster = computed(() => demoRoster(replay.start.value));
const replaying = computed(() => mode.value === "replay");

let loop = 0;
let queue = loopEvents(start, 0);
const timer = setInterval(() => {
  const now = Date.now();
  const due = queue.filter((e) => (e.at as number) <= now);
  if (!due.length) return;
  queue = queue.filter((e) => (e.at as number) > now);
  if (!queue.length) {
    loop++;
    queue = loopEvents(start + loop * LOOP_MS, loop * 100);
  }
  live.value = [...live.value, ...due].slice(-400);
}, 200);
onBeforeUnmount(() => clearInterval(timer));
document.title = "办公室 · 演示";
document.body.style.background = "#1E2027";
const tab = (on: boolean) => ({ height: "28px", padding: "0 10px", borderRadius: "7px", border: "none", cursor: "pointer", background: on ? "#F0EDE6" : "#2A2D36", color: on ? "#1F1E1C" : "#E6E3DC" });
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 12px; padding: 18px; color: #e6e3dc; font-size: 13px">
    <div style="display: flex; gap: 8px; align-items: center">
      <strong style="font-size: 15px">办公室</strong>
      <button type="button" :style="tab(!replaying)" @click="mode = 'live'">实时</button>
      <button type="button" :style="tab(replaying)" @click="mode = 'replay'">回放</button>
      <span v-if="focus !== null">聚焦 #{{ focus }} <button type="button" :style="tab(false)" @click="focus = null">全部</button></span>
      <span v-if="selected" style="color: #a8a49b">选中 {{ selected }}</span>
    </div>
    <div v-if="hot.error" style="color: #f2a65e; font-size: 12px">布局未生效：{{ hot.error }}</div>
    <Office
      :key="mode"
      :layout="hot.layout"
      :employees="replaying ? replayRoster : roster"
      :threads="DEMO_THREADS"
      :events="replaying ? replay.events.value : live"
      :focus="focus"
      :selected="selected"
      :data="DEMO_DATA"
      :task-titles="DEMO_TITLES"
      :speed="replaying ? replay.speed.value : 1"
      :now="replaying ? replay.time.value : undefined"
      :seek-key="replaying ? replay.seekKey.value : undefined"
      @focus-thread="(no: number | null) => (focus = no)"
      @select="(id: string | null) => (selected = id)"
      @action="(a: unknown) => console.info('office action', a)"
    />
    <ReplayBar v-if="replaying" :replay="replay" style="max-width: 640px" />
  </div>
</template>
