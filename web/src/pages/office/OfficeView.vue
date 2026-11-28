<script setup lang="ts">
// The pixel office driven by the snapshot plus live `status` frames from the project stream,
// with the dark side panel, scope switch, employee card and replay.
import { $t } from "@/i18n";
import { useQuery } from "@tanstack/vue-query";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { layoutTickKey, statusLogKey, type StatusFrame } from "@/api/events";
import type { OfficeSnapshot } from "@/api/views";
import { officeRoster, staffSummary, staffSummaryText, toOfficeEvents, toOfficeThreads, toWidgetData } from "@/lib/office";
import { Office, useRemoteLayout, type OfficeAction } from "@/office";
import { foldStatuses } from "@/office/replay";
import { actionTarget, DARK, liveThreads } from "./logic";
import OfficeHeader from "./OfficeHeader.vue";
import OfficeSide from "./OfficeSide.vue";
import ReplayOffice from "./ReplayOffice.vue";

const props = defineProps<{ pid: string; name: string; snapshot: OfficeSnapshot; layoutId: string }>();
const router = useRouter();
// The status feed starts at the first snapshot and only grows (a shorter feed would teleport everyone);
// the roster follows each refetched snapshot (officeRoster skips the frames it already has).
const feedStart = props.snapshot.last_event_id;
const log = useQuery<StatusFrame[]>({ queryKey: computed(() => statusLogKey(props.pid)), queryFn: () => [], staleTime: Infinity, initialData: [] });
const tick = useQuery<number>({ queryKey: computed(() => layoutTickKey(props.pid)), queryFn: () => 0, staleTime: Infinity, initialData: 0 });
const live = computed(() => toOfficeEvents(log.data.value ?? [], feedStart));
const remote = useRemoteLayout(() => props.layoutId);
watch(() => tick.data.value, (n) => n && remote.reload());
const focus = ref<number | null>(null);
const selected = ref<string | null>(null);
const replaying = ref(false);
const threads = computed(() => liveThreads(props.snapshot.threads));
const onAction = (a: OfficeAction) => {
  const to = actionTarget(props.pid, a, focus.value);
  if (to) void router.push(to);
};
const common = computed(() => ({
  layout: remote.layout.value,
  threads: toOfficeThreads(threads.value),
  focus: focus.value,
  onFocusThread: (no: number | null) => (focus.value = no),
  selected: selected.value,
  onSelect: (id: string | null) => (selected.value = id),
  data: toWidgetData(props.snapshot, focus.value),
  onAction,
}));
const employees = computed(() => officeRoster(props.snapshot, log.data.value ?? []));
const summary = computed(() => staffSummaryText(staffSummary(employees.value, foldStatuses(employees.value, live.value), threads.value)));
</script>

<template>
  <div class="flex min-h-full flex-wrap text-[#F0EDE6]" :style="{ background: DARK.page }">
    <section :aria-label="$t('办公室场景')" class="flex min-w-0 flex-[999_1_820px] flex-col gap-3 p-[18px]">
      <OfficeHeader :pid="pid" :name="name" :threads="threads" :focus="focus" :replaying="replaying" :summary="summary" @focus="(n) => (focus = n)" />
      <div v-if="remote.error.value" class="text-xs text-[#F2A65E]">{{ $t("布局未生效：{error}", { error: remote.error.value }) }}</div>
      <ReplayOffice v-if="replaying" :pid="pid" :common="common" />
      <Office v-else v-bind="common" :employees="employees" :events="live" />
    </section>
    <OfficeSide
      :pid="pid"
      :snapshot="snapshot"
      :threads="threads"
      :focus="focus"
      :events="live"
      :selected="selected"
      :layout="remote.layout.value"
      :replaying="replaying"
      @replaying="(on) => (replaying = on)"
    />
  </div>
</template>
