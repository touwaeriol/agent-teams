<script setup lang="ts">
// 环境: the repositories every thread includes, whether employees work in git worktrees, and the
// remote servers (设置 → 服务器) employees of which roles may reach over SSH. Employees always run on
// your own nodes (设置 → 节点).
import { $t } from "@/i18n";
import type { ProjectView } from "@/api/views";
import { WORKTREE_CHOICES, worktreeChoice, type WorkMode } from "@/lib/projectSettings";
import Heading from "./Heading.vue";
import RepoSection from "./RepoSection.vue";
import ServersSection from "./ServersSection.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
</script>

<template>
  <div class="flex flex-col gap-3">
    <Heading>{{ $t("环境") }}</Heading>
    <RepoSection :project="project" />
    <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 pt-5">
      <span class="font-medium">{{ $t("工作树") }}</span>
      <el-select
        :model-value="project.work_mode"
        :aria-label="$t('工作树')"
        class="at-select-36 at-raised !w-60"
        @update:model-value="(v: WorkMode) => update.mutate({ work_mode: v })"
      >
        <el-option v-for="c in WORKTREE_CHOICES" :key="c.value" :value="c.value" :label="c.label" />
      </el-select>
    </div>
    <p class="text-[13px] leading-5 text-muted-foreground">{{ $t("{desc} 已有检出的员工在下次准备工作目录时按新方式；最好在没有进行中的讨论串时修改。", { desc: worktreeChoice(project.work_mode).desc }) }}</p>
    <ServersSection :project-id="project.id" />
  </div>
</template>
