<script setup lang="ts">
/** Only shown when some role asks (逐条问): bypass roles never raise approval cards. */
import type { ProjectView } from "@/api/views";
import { APPROVAL_ROUTING_DESC, LEAD_TIMEOUTS, leadTimeoutLabel } from "./logic";
import Row from "./Row.vue";
import SwitchRow from "./SwitchRow.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
</script>

<template>
  <SwitchRow
    title="新讨论串默认“全部放行”"
    desc="只影响设成“逐条问”的成员：打开后它们的操作也不再询问。"
    :checked="project.auto_approve"
    @change="(v: boolean) => update.mutate({ auto_approve: v })"
  />
  <Row title="审批方式" :desc="`只影响设成“逐条问”的成员。${APPROVAL_ROUTING_DESC[project.delivery.approval_routing]}`">
    <el-select
      :model-value="project.delivery.approval_routing"
      aria-label="审批方式"
      class="at-select-36 at-raised !w-[236px]"
      @update:model-value="(v: 'lead_first' | 'user') => update.mutate({ delivery: { approval_routing: v } })"
    >
      <el-option value="lead_first" label="组长先判断，拿不准再问我" />
      <el-option value="user" label="全部问我" />
    </el-select>
  </Row>
  <Row v-if="project.delivery.approval_routing === 'lead_first'" title="组长判断时限" desc="组长在这段时间内没有判断，请求就转给你。">
    <el-select
      :model-value="String(project.delivery.lead_review_timeout_s)"
      aria-label="组长判断时限"
      class="at-select-36 at-raised !w-36"
      @update:model-value="(v: string) => update.mutate({ delivery: { lead_review_timeout_s: Number(v) } })"
    >
      <template #label>{{ leadTimeoutLabel(project.delivery.lead_review_timeout_s) }}</template>
      <el-option v-for="s in LEAD_TIMEOUTS" :key="s" :value="String(s)" :label="leadTimeoutLabel(s)" />
    </el-select>
  </Row>
</template>
