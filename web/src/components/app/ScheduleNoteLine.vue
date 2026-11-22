<script setup lang="ts">
// The centred line of a schedule an agent just set (schedule_create): 撤销 deletes it while it
// is still waiting to fire.
import { computed } from "vue";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSchedules } from "@/api/queries";
import type { MessageView } from "@/api/views";

const props = defineProps<{ pid: string; message: MessageView }>();
const id = computed(() => (props.message.meta?.schedule_undo ? props.message.meta.schedule_id : undefined));
const list = useSchedules(() => props.pid, () => Boolean(id.value));
const pending = computed(() => (list.data.value ?? []).some((s) => s.id === id.value && s.status === "active"));
const undo = useAction(() => scheduleActions.remove(id.value ?? ""), { invalidate: () => [keys.schedules(props.pid)], success: "已撤销" });
</script>

<template>
  <p class="text-center text-xs text-muted-foreground" data-schedule-note>
    {{ message.content }}
    <button v-if="pending" type="button" class="ml-1 text-primary hover:underline underline-offset-2" :disabled="undo.isPending.value" @click="undo.mutate()">撤销</button>
  </p>
</template>
