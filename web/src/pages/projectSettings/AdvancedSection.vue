<script setup lang="ts">
// 高级: approvals (only when some role asks), ask notifications, acceptance, workflow edits, merge method.
import { computed } from "vue";
import { useRoles } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import { usesAsk } from "@/lib/roles";
import ApprovalRows from "./ApprovalRows.vue";
import Heading from "./Heading.vue";
import Row from "./Row.vue";
import SwitchRow from "./SwitchRow.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
const roles = useRoles();
const ask = computed(() => usesAsk(roles.data.value?.roles ?? []));
const MERGE = [
  { value: "squash", label: "squash" },
  { value: "merge", label: "merge commit" },
  { value: "rebase", label: "rebase" },
];
</script>

<template>
  <div class="flex flex-col">
    <Heading>高级</Heading>
    <span class="pb-1 text-[13px] text-muted-foreground">审批与交付</span>
    <span v-if="!ask" class="pb-2 text-xs leading-[18px] text-muted-foreground">成员都是“直接执行”，不会弹权限确认。有岗位改成“逐条问”时，这里才有审批方式可设。</span>
    <ApprovalRows v-else :project="project" />
    <SwitchRow
      title="成员提问时通知我"
      desc="员工用“问你”提问时，侧栏和项目首页都会提醒。"
      :checked="project.delivery.notify_on_ask"
      @change="(v: boolean) => update.mutate({ delivery: { notify_on_ask: v } })"
    />
    <SwitchRow title="讨论串完成后需要我验收" desc="关闭后 PR 一通过检查就自动合并到基准分支。" :checked="project.require_acceptance" @change="(v: boolean) => update.mutate({ require_acceptance: v })" />
    <SwitchRow
      title="允许员工修改 workflow"
      desc="允许成员改 .github/workflows 下的文件（默认不允许）。"
      :checked="project.delivery.allow_workflow_edits"
      @change="(v: boolean) => update.mutate({ delivery: { allow_workflow_edits: v } })"
    />
    <Row title="合并方式" desc="讨论串 PR 合并到基准分支时用。">
      <el-select
        :model-value="project.delivery.merge_method"
        aria-label="合并方式"
        class="at-select-36 at-raised !w-36"
        @update:model-value="(v: 'squash' | 'merge' | 'rebase') => update.mutate({ delivery: { merge_method: v } })"
      >
        <el-option v-for="m in MERGE" :key="m.value" :value="m.value" :label="m.label" />
      </el-select>
    </Row>
  </div>
</template>
