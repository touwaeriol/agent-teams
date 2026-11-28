<script setup lang="ts">
// A schedule's last runs (newest first): when, whether it went out, and why not.
import { $t } from "@/i18n";
import { useScheduleRuns } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { formatDateTime } from "@/lib/library";
import { runOutcome } from "@/lib/schedules";
import { cn } from "@/lib/utils";

const props = defineProps<{ pid: string; id: string }>();
const runs = useScheduleRuns(() => props.pid, () => props.id);
</script>

<template>
  <div class="mt-1.5 flex flex-col gap-1 border-t pt-2 text-xs" data-schedule-runs>
    <Loading v-if="runs.isLoading.value" />
    <p v-else-if="(runs.data.value ?? []).length === 0" class="text-muted-foreground">{{ $t("还没有运行过") }}</p>
    <p v-for="r in runs.data.value ?? []" :key="r.id" class="flex flex-wrap gap-x-2 text-muted-foreground">
      <span>{{ formatDateTime(r.fired_at) }}</span>
      <span :class="cn(r.outcome === 'delivered' ? 'text-foreground' : r.outcome === 'failed' ? 'text-destructive' : '')">{{ runOutcome(r.outcome) }}</span>
      <span v-if="r.manual">{{ $t("手动运行") }}</span>
      <span v-else-if="r.planned_at && Date.parse(r.fired_at) - Date.parse(r.planned_at) >= 120_000">{{ $t("原定 {planned_at}", { planned_at: formatDateTime(r.planned_at) }) }}</span>
      <span v-if="r.error" class="basis-full">{{ r.error }}</span>
    </p>
  </div>
</template>
