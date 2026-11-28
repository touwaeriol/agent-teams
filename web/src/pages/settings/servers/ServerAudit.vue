<script setup lang="ts">
import { $t } from "@/i18n";
/** 访问记录 of one server, filterable by type and employee. */
import { computed, reactive } from "vue";
import { useServerAudit } from "@/api/queries";
import SshAuditTable from "@/components/app/servers/SshAuditTable.vue";
import { AUDIT_KINDS } from "@/lib/servers";

const props = defineProps<{ serverId: string }>();
const filter = reactive({ kind: "", employee: "" });
const audit = useServerAudit(() => props.serverId, () => ({ ...filter }));
const rows = computed(() => audit.data.value?.pages.flatMap((p) => p.items) ?? []);
</script>

<template>
  <div class="flex flex-col gap-2.5">
    <div class="flex flex-wrap gap-2">
      <el-select v-model="filter.kind" clearable :placeholder="$t('全部类型')" :aria-label="$t('类型')" class="at-select-36 !w-36">
        <el-option v-for="k in AUDIT_KINDS" :key="k.value" :value="k.value" :label="k.label" />
      </el-select>
      <el-input v-model.trim="filter.employee" clearable :placeholder="$t('员工，如 dev-1')" :aria-label="$t('员工')" class="!w-44" />
    </div>
    <SshAuditTable :rows="rows" :show-server="false" :has-more="audit.hasNextPage.value" :loading="audit.isFetchingNextPage.value" @more="audit.fetchNextPage()" />
  </div>
</template>
