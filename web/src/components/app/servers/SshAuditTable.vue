<script setup lang="ts">
import { $t } from "@/i18n";
/** The SSH access log: who, which session / thread, when, which server, the command and the result. */
import { computed } from "vue";
import type { SshAuditView } from "@/api/views";
import DataTable, { type Column } from "@/components/common/DataTable.vue";
import { auditKindLabel, auditResult, auditWho } from "@/lib/servers";
import { timeAgo } from "@/lib/utils";

const props = withDefaults(defineProps<{ rows: SshAuditView[]; showServer?: boolean; threadLabel?: (threadId: string) => string; hasMore?: boolean; loading?: boolean }>(), {
  showServer: true,
  threadLabel: undefined,
});
const emit = defineEmits<{ more: [] }>();
const columns = computed<Column[]>(() => [
  { key: "time", header: $t("时间"), class: "whitespace-nowrap" },
  { key: "who", header: $t("谁 · 讨论串 / 会话"), class: "whitespace-nowrap" },
  ...(props.showServer ? [{ key: "server", header: $t("服务器"), class: "whitespace-nowrap" }] : []),
  { key: "kind", header: $t("类型"), class: "whitespace-nowrap" },
  { key: "command", header: $t("命令") },
  { key: "result", header: $t("结果"), class: "min-w-[110px]" },
]);
const TONE = { ok: "text-ok", warn: "text-warn", danger: "text-bad", muted: "text-muted-foreground" };
const where = (a: SshAuditView) => {
  const t = a.thread_id ? (props.threadLabel?.(a.thread_id) ?? a.thread_id) : "";
  return [t, a.session_id].filter(Boolean).join(" · ") || "—";
};
</script>

<template>
  <div class="flex flex-col gap-2">
    <DataTable :columns="columns" :rows="rows" :row-key="(a) => String(a.id)" :min-width="showServer ? 640 : 620" :empty="$t('还没有访问记录')">
      <template #cell-time="{ row }"><span :title="new Date(row.created_at).toLocaleString('zh-CN')">{{ timeAgo(row.created_at) }}</span></template>
      <template #cell-who="{ row }">
        <span class="flex flex-col">
          <span>{{ auditWho(row) }}</span>
          <span class="font-mono text-xs text-muted-foreground">{{ where(row) }}</span>
        </span>
      </template>
      <template #cell-server="{ row }"><span class="font-mono">{{ row.server_name || "—" }}</span></template>
      <template #cell-kind="{ row }">{{ auditKindLabel(row.kind) }}</template>
      <template #cell-command="{ row }">
        <code v-if="row.command" :class="['block truncate font-mono text-xs', showServer ? 'max-w-[200px]' : 'max-w-[300px]']" :title="row.command">{{ row.command }}</code>
        <span v-else class="text-muted-foreground">—</span>
        <span v-if="row.command_truncated" class="text-xs text-muted-foreground">{{ $t("（已截断）") }}</span>
      </template>
      <template #cell-result="{ row }"><span :class="TONE[auditResult(row).tone]">{{ auditResult(row).text }}</span></template>
    </DataTable>
    <el-button v-if="hasMore" size="small" class="self-center" :loading="loading" @click="emit('more')">{{ $t("加载更多") }}</el-button>
  </div>
</template>
