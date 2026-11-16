<script setup lang="ts">
// 环境: repositories, work mode, branch naming, cleanup, and the nodes employees run on.
import type { ProjectView } from "@/api/views";
import ChoiceCards from "@/components/app/ChoiceCards.vue";
import Divider from "./Divider.vue";
import Heading from "./Heading.vue";
import { MODES } from "./logic";
import NodesList from "./NodesList.vue";
import RepoSection from "./RepoSection.vue";
import Row from "./Row.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
</script>

<template>
  <div class="flex flex-col gap-2">
    <RepoSection :project="project" />
    <Divider />
    <Heading>工作方式</Heading>
    <ChoiceCards :model-value="project.work_mode" label="工作方式" :options="MODES" @update:model-value="(v) => update.mutate({ work_mode: v })" />
    <Row title="讨论串分支命名" desc="每个仓库各开一条讨论串分支">
      <span class="font-mono text-[13px] text-muted-foreground">thread/{编号}-{标题}</span>
    </Row>
    <Row title="讨论串结束后" desc="有未推送改动时保留并提醒">
      <span class="text-[13px] text-muted-foreground">清理 worktree</span>
    </Row>
    <Divider />
    <NodesList />
  </div>
</template>
