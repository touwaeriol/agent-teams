<script setup lang="ts">
/** The dark “调度看板”: live / replay switch, what waits, the selected person, recent events, legend. */
import { computed } from "vue";
import { useWaiting } from "@/api/queries";
import type { OfficeSnapshot, ThreadView } from "@/api/views";
import { boardSummary } from "@/lib/office";
import type { OfficeLayout, OfficeStatusEvent } from "@/office";
import { STATE_LABELS } from "@/office/overlay";
import EmployeeCard from "./EmployeeCard.vue";
import { boardItems, DARK, hhmmss, recentEvents, whoColor } from "./logic";

const props = defineProps<{ pid: string; snapshot: OfficeSnapshot; threads: ThreadView[]; focus: number | null; events: OfficeStatusEvent[]; selected: string | null; layout: OfficeLayout; replaying: boolean }>();
const emit = defineEmits<{ replaying: [on: boolean] }>();
const waiting = useWaiting(() => props.pid);
const board = computed(() => boardSummary(props.threads, props.focus));
const items = computed(() => boardItems(props.threads, waiting.data.value?.questions ?? [], props.snapshot.board.claimable, props.focus));
const roster = computed(() => new Map(props.snapshot.employees.map((e) => [e.id, e])));
const recent = computed(() =>
  recentEvents(props.events, roster.value, props.focus).map((e) => {
    const who = roster.value.get(String(e.employee));
    return { e, where: who?.thread_no ? `#${who.thread_no}` : "统筹", name: who?.name ?? String(e.employee), color: whoColor(who), what: e.status.bubble || STATE_LABELS[e.status.state] || e.status.state };
  }),
);
const btn = (on: boolean) => ({ height: "30px", padding: "0 10px", borderRadius: "8px", border: on ? "none" : "1px solid #4A4D59", background: on ? "#C8622A" : "transparent", color: on ? "#FFFFFF" : "#E6E3DC", fontSize: "12px" });
</script>

<template>
  <aside aria-label="事件流" class="flex min-h-screen min-w-0 max-w-[380px] flex-[1_1_320px] flex-col gap-3.5 border-l p-[18px]" :style="{ background: DARK.panel, borderColor: DARK.line }">
    <span class="flex items-center justify-between">
      <span class="text-base font-semibold">调度看板</span>
      <span class="flex gap-1.5">
        <button type="button" :style="btn(!replaying)" @click="emit('replaying', false)">实时</button>
        <button type="button" :style="btn(replaying)" @click="emit('replaying', true)">回放…</button>
      </span>
    </span>
    <div class="flex flex-col gap-1.5 rounded-[10px] border px-3 py-2.5 text-xs" :style="{ background: DARK.page, borderColor: DARK.line }">
      <span class="flex justify-between">
        <span class="text-[#F2C14E]">{{ board.title }}</span>
        <span :style="{ color: DARK.soft }">{{ board.count }}</span>
      </span>
      <span class="flex h-1" :style="{ background: DARK.line }"><span :style="{ width: board.pct, background: '#5FD08A' }" /></span>
      <span v-if="items.length === 0" :style="{ color: DARK.soft }">现在没有等待中的事</span>
      <span v-for="i in items" :key="i.state + i.text" class="flex gap-2 leading-[18px]">
        <span class="min-w-10" :style="{ color: i.color }">{{ i.state }}</span>
        <span :style="{ color: DARK.text }">{{ i.text }}</span>
      </span>
    </div>
    <EmployeeCard :id="selected" />
    <span class="text-xs" :style="{ color: DARK.soft }">点人物看他在做什么；点工作组门牌聚焦那个讨论串。</span>
    <div class="flex min-h-[120px] flex-1 flex-col gap-2.5 overflow-y-auto">
      <span v-if="recent.length === 0" class="text-xs text-[#7E8190]">等待事件…</span>
      <div v-for="r in recent" :key="r.e.seq" class="flex flex-col gap-0.5 text-xs leading-[18px]">
        <span class="font-mono text-[#7E8190]">{{ hhmmss(r.e.at) }} · {{ r.where }}</span>
        <span :style="{ color: DARK.text }"><span class="font-mono" :style="{ color: r.color }">{{ r.name }}</span> {{ r.what }}</span>
      </div>
    </div>
    <div class="flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-3 text-xs" :style="{ borderColor: DARK.line, color: DARK.text }">
      <span v-for="l in layout.legend ?? []" :key="l.label" class="flex items-center gap-1.5"><span class="size-2.5" :style="{ background: l.color }" />{{ l.label }}</span>
    </div>
  </aside>
</template>
