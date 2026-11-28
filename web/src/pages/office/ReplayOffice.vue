<script setup lang="ts">
import { $t } from "@/i18n";
/** The last hour from /office/replay, played back with the same renderer. */
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import { client, unwrap } from "@/api/client";
import { errorText } from "@/api/mutations";
import Loading from "@/components/common/Loading.vue";
import { replayEvents, toOfficeEmployees } from "@/lib/office";
import { Office, ReplayBar, useReplay, type OfficeProps } from "@/office";
import { DARK, lastHour } from "./logic";

const props = defineProps<{ pid: string; common: Omit<OfficeProps, "employees" | "events"> }>();
const range = lastHour();
const replay = useQuery({
  queryKey: computed(() => ["p", props.pid, "replay", range.from]),
  queryFn: () => unwrap(client.GET("/api/projects/{pid}/office/replay", { params: { path: { pid: props.pid }, query: range } })),
});
const events = computed(() => replayEvents(replay.data.value?.events ?? []));
const r = useReplay(events, Date.parse(range.from), Date.parse(range.to));
const employees = computed(() => (replay.data.value ? toOfficeEmployees(replay.data.value.snapshot) : []));
const barStyle = { background: DARK.panel, border: `1px solid ${DARK.line}`, borderRadius: "10px", padding: "10px 12px" };
</script>

<template>
  <div v-if="replay.error.value" class="text-sm text-[#F2A65E]">{{ $t("回放数据取不到：{error}", { error: errorText(replay.error.value) }) }}</div>
  <Loading v-else-if="!replay.data.value" />
  <template v-else>
    <ReplayBar :replay="r" :style="barStyle" />
    <Office v-bind="common" :employees="employees" :events="r.events.value" :now="r.time.value" :speed="r.speed.value" :seek-key="r.seekKey.value" />
  </template>
</template>
