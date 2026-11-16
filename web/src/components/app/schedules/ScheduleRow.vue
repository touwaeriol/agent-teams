<script setup lang="ts">
import { Pencil, Play, Trash2 } from "lucide-vue-next";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ScheduleView } from "@/api/views";
import IconButton from "@/components/common/IconButton.vue";
import { formatDateTime } from "@/lib/library";
import { describeCron } from "@/lib/schedules";

const props = defineProps<{ pid: string; item: ScheduleView }>();
const emit = defineEmits<{ edit: [] }>();
const invalidate = () => [keys.schedules(props.pid)];
const toggle = useAction((enabled: boolean) => scheduleActions.update(props.item.id, { enabled }), { invalidate });
const run = useAction(() => scheduleActions.run(props.item.id), { invalidate, success: "已发给统筹" });
const remove = useAction(() => scheduleActions.remove(props.item.id), { invalidate, success: "已删除" });
</script>

<template>
  <div class="flex items-start gap-3 rounded-xl border bg-panel px-3.5 py-3">
    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="flex items-center gap-2">
        <span class="truncate font-medium">{{ item.name }}</span>
        <span class="shrink-0 rounded-md bg-raised px-1.5 py-px text-xs text-muted-foreground">{{ describeCron(item.cron) }}</span>
      </span>
      <span class="line-clamp-2 text-[13px] text-muted-foreground">{{ item.message }}</span>
      <span class="text-xs text-muted-foreground">
        {{ item.enabled && item.next_run_at ? `下次 ${formatDateTime(item.next_run_at)}` : "已停用" }}{{ item.last_run_at ? ` · 上次 ${formatDateTime(item.last_run_at)}` : "" }}
      </span>
      <span v-if="item.last_error" class="text-xs text-destructive">上次没发出去：{{ item.last_error }}</span>
    </span>
    <span class="flex shrink-0 items-center gap-0.5">
      <IconButton label="现在运行一次" :disabled="run.isPending.value" @click="run.mutate()"><Play :stroke-width="1.8" /></IconButton>
      <IconButton label="编辑" @click="emit('edit')"><Pencil :stroke-width="1.8" /></IconButton>
      <IconButton label="删除" @click="remove.mutate()"><Trash2 :stroke-width="1.8" /></IconButton>
      <el-switch :model-value="item.enabled" :aria-label="`启用 ${item.name}`" class="at-switch-blue ml-1.5" @change="(v: string | number | boolean) => toggle.mutate(Boolean(v))" />
    </span>
  </div>
</template>
