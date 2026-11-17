<script setup lang="ts">
// 高级: approvals (only when some role asks), ask notifications, how threads with PRs end (merge_policy),
// workflow edits, merge method,
// and how thread branches are named and cleaned up (fixed by the platform, shown for reference).
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
const POLICY: { value: ProjectView["merge_policy"]; label: string }[] = [
  { value: "pr", label: "提交 PR 即完成" },
  { value: "auto", label: "检查通过后自动合并" },
  { value: "manual", label: "等我验收" },
];
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
    <Row title="讨论串开出 PR 后" desc="提交 PR 即完成：检查通过就结束，PR 留给你在 GitHub 上合并。自动合并：检查通过后直接合并进基准分支。等我验收：停在待验收，你点了才合并。检查没过都会交回员工修。">
      <el-select
        :model-value="project.merge_policy"
        aria-label="讨论串开出 PR 后"
        class="at-select-36 at-raised !w-44"
        @update:model-value="(v: ProjectView['merge_policy']) => update.mutate({ merge_policy: v })"
      >
        <el-option v-for="m in POLICY" :key="m.value" :value="m.value" :label="m.label" />
      </el-select>
    </Row>
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
    <span class="pb-1 pt-4 text-[13px] text-muted-foreground">分支</span>
    <Row title="讨论串分支命名" desc="每个有改动的仓库各开一条讨论串分支，成员分支挂在它下面。">
      <span class="font-mono text-[13px] text-muted-foreground">thread/{编号}-{标题}</span>
    </Row>
    <Row title="讨论串结束后" desc="有未推送改动时保留并提醒。">
      <span class="text-[13px] text-muted-foreground">清理工作目录</span>
    </Row>
  </div>
</template>
