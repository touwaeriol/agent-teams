<script setup lang="ts">
import { $t } from "@/i18n";
import { History, Pencil, Play, Trash2 } from "lucide-vue-next";
import { computed, ref } from "vue";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ScheduleView } from "@/api/views";
import IconButton from "@/components/common/IconButton.vue";
import { formatDateTime } from "@/lib/library";
import { describeSchedule, scheduleCreator, scheduleStatus, scheduleTarget } from "@/lib/schedules";
import ScheduleRuns from "./ScheduleRuns.vue";

const props = defineProps<{ pid: string; item: ScheduleView }>();
const emit = defineEmits<{ edit: [] }>();
const invalidate = () => [keys.schedules(props.pid)];
const history = ref(false);
const toggle = useAction((enabled: boolean) => scheduleActions.update(props.item.id, { enabled }), { invalidate });
const run = useAction(() => scheduleActions.run(props.item.id), { invalidate, success: $t("已发出") });
const remove = useAction(() => scheduleActions.remove(props.item.id), { invalidate, success: $t("已删除") });
const active = computed(() => props.item.status === "active");
const when = computed(() => {
  if (!active.value) return `${scheduleStatus(props.item.status)}${props.item.ended_at ? ` · ${formatDateTime(props.item.ended_at)}` : ""}`;
  const next = props.item.enabled && props.item.next_run_at ? $t("下次 {next_run_at}", { next_run_at: formatDateTime(props.item.next_run_at) }) : $t("已停用");
  return next + (props.item.last_run_at ? $t(" · 上次 {last_run_at}", { last_run_at: formatDateTime(props.item.last_run_at) }) : "");
});
</script>

<template>
  <div class="flex items-start gap-3 rounded-xl border bg-panel px-3.5 py-3" :class="{ 'opacity-60': !active }">
    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span class="truncate font-medium">{{ item.name }}</span>
        <span class="shrink-0 rounded-md bg-raised px-1.5 py-px text-xs text-muted-foreground">{{ describeSchedule(item) }}</span>
        <span class="shrink-0 rounded-md bg-raised px-1.5 py-px text-xs text-muted-foreground">{{ $t("发给{item}", { item: scheduleTarget(item) }) }}</span>
        <span v-if="item.created_by !== 'user'" class="shrink-0 rounded-md bg-primary/10 px-1.5 py-px text-xs text-primary">{{ $t("{item}设置", { item: scheduleCreator(item) }) }}</span>
      </span>
      <span class="line-clamp-2 text-[13px] text-muted-foreground">{{ item.message }}</span>
      <span class="text-xs text-muted-foreground">{{ when }}</span>
      <span v-if="item.last_error" class="text-xs text-destructive">{{ $t("上次没发出去：{last_error}", { last_error: item.last_error }) }}</span>
      <ScheduleRuns v-if="history" :pid="pid" :id="item.id" />
    </span>
    <span class="flex shrink-0 items-center gap-0.5">
      <IconButton :label="history ? $t('收起运行记录') : $t('运行记录')" :active="history" @click="history = !history"><History :stroke-width="1.8" /></IconButton>
      <template v-if="active">
        <IconButton :label="$t('现在运行一次')" :disabled="run.isPending.value" @click="run.mutate()"><Play :stroke-width="1.8" /></IconButton>
        <IconButton :label="$t('编辑')" @click="emit('edit')"><Pencil :stroke-width="1.8" /></IconButton>
      </template>
      <IconButton :label="$t('删除')" @click="remove.mutate()"><Trash2 :stroke-width="1.8" /></IconButton>
      <el-switch
        v-if="active"
        :model-value="item.enabled"
        :aria-label="$t('启用 {name}', { name: item.name })"
        class="at-switch-blue ml-1.5"
        @change="(v: string | number | boolean) => toggle.mutate(Boolean(v))"
      />
    </span>
  </div>
</template>
