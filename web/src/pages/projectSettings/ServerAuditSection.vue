<script setup lang="ts">
// 环境 → 访问记录: every SSH connection, command and refusal of this project's employees, newest
// first, filterable by server, type and employee.
import { computed, reactive } from "vue";
import { useProjectServerAudit, useThreads } from "@/api/queries";
import type { ServerView } from "@/api/views";
import SshAuditTable from "@/components/app/servers/SshAuditTable.vue";
import { AUDIT_KINDS } from "@/lib/servers";
import Heading from "./Heading.vue";

const props = defineProps<{ projectId: string; servers: ServerView[] }>();
const filter = reactive({ server_id: "", kind: "", employee: "" });
const audit = useProjectServerAudit(() => props.projectId, () => ({ ...filter }));
const rows = computed(() => audit.data.value?.pages.flatMap((p) => p.items) ?? []);
const threads = useThreads(() => props.projectId);
const threadNo = computed(() => new Map((threads.data.value ?? []).map((t) => [t.id, t.no])));
const threadLabel = (id: string) => (threadNo.value.has(id) ? `#${threadNo.value.get(id)}` : id);
</script>

<template>
  <div class="flex flex-col gap-2.5 pt-4" data-section="server-audit">
    <Heading>访问记录</Heading>
    <div class="flex flex-wrap gap-2">
      <el-select v-model="filter.server_id" clearable placeholder="全部服务器" aria-label="服务器" class="at-select-36 !w-36">
        <el-option v-for="s in servers" :key="s.id" :value="s.id" :label="s.name" />
      </el-select>
      <el-select v-model="filter.kind" clearable placeholder="全部类型" aria-label="类型" class="at-select-36 !w-36">
        <el-option v-for="k in AUDIT_KINDS.filter((k) => k.value !== 'test')" :key="k.value" :value="k.value" :label="k.label" />
      </el-select>
      <el-input v-model.trim="filter.employee" clearable placeholder="员工，如 dev-1" aria-label="员工" class="!w-44" />
    </div>
    <SshAuditTable :rows="rows" :thread-label="threadLabel" :has-more="audit.hasNextPage.value" :loading="audit.isFetchingNextPage.value" @more="audit.fetchNextPage()" />
  </div>
</template>
