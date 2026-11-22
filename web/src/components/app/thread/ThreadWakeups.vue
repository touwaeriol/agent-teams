<script setup lang="ts">
// The wake-ups waiting to fire in this thread (the lead's, its members', and the ones the
// coordinator or you aimed at it), above the message box; ✕ deletes one.
import { AlarmClock, X } from "lucide-vue-next";
import { computed } from "vue";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSchedules } from "@/api/queries";
import IconButton from "@/components/common/IconButton.vue";
import { formatDateTime } from "@/lib/library";
import { describeSchedule, scheduleCreator, scheduleTarget } from "@/lib/schedules";

const props = defineProps<{ pid: string; threadId: string }>();
const list = useSchedules(() => props.pid);
const items = computed(() => (list.data.value ?? []).filter((s) => s.thread_id === props.threadId && s.status === "active" && s.enabled));
const remove = useAction((id: string) => scheduleActions.remove(id), { invalidate: () => [keys.schedules(props.pid)], success: "已取消" });
</script>

<template>
  <div v-if="items.length" class="flex flex-col gap-1 rounded-xl border bg-panel px-3 py-2 text-xs" data-thread-wakeups>
    <p v-for="s in items" :key="s.id" class="flex items-center gap-2 text-muted-foreground">
      <AlarmClock class="size-3.5 shrink-0" :stroke-width="1.8" />
      <span class="min-w-0 flex-1 truncate">
        <span class="font-medium text-foreground">{{ s.name }}</span>
        · {{ s.next_run_at ? formatDateTime(s.next_run_at) : describeSchedule(s) }} 唤醒{{ scheduleTarget(s) }}
        · {{ scheduleCreator(s) }}设置
      </span>
      <IconButton :label="`取消 ${s.name}`" class="!size-6" :disabled="remove.isPending.value" @click="remove.mutate(s.id)"><X :stroke-width="1.8" /></IconButton>
    </p>
  </div>
</template>
