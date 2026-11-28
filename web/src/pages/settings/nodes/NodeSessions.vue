<script setup lang="ts">
import { $t } from "@/i18n";
import { computed } from "vue";
import { useNodeSessions } from "@/api/queries";

const props = defineProps<{ nodeId: string }>();
const sessions = useNodeSessions(() => props.nodeId);
const list = computed(() => sessions.data.value ?? []);
</script>

<template>
  <div class="flex flex-col gap-1.5">
    <span class="text-xs text-muted-foreground">{{ $t("正在运行") }}</span>
    <span v-if="list.length === 0" class="text-[13px] text-muted-foreground">{{ $t("没有会话") }}</span>
    <span v-for="s in list" :key="s.session_id" class="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 rounded-lg bg-background px-2.5 py-2 text-[13px]">
      <span class="font-mono">{{ s.employee }}</span>
      <span class="text-muted-foreground">{{ s.thread_no ? `#${s.thread_no} · ` : "" }}{{ s.project }}</span>
      <span class="ml-auto font-mono text-xs text-muted-foreground">{{ s.state }}</span>
    </span>
  </div>
</template>
