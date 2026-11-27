<script setup lang="ts">
// 记忆: 项目指令 (written by you) and 自动记忆 (MEMORY.md + topic files). With the shared folder on
// they are files in the project's .memory/ the employees edit with their own file tools (or the
// memory_* tools). Every new session reads the instructions and MEMORY.md.
import type { ProjectView } from "@/api/views";
import Divider from "./Divider.vue";
import Heading from "./Heading.vue";
import InstructionsField from "./InstructionsField.vue";
import MemoryFileList from "@/components/memory/MemoryFileList.vue";

defineProps<{ project: ProjectView }>();
</script>

<template>
  <div class="flex flex-col">
    <Heading>记忆</Heading>
    <InstructionsField :project="project" />
    <Divider />
    <div class="flex flex-col gap-0.5 pt-2">
      <h3 class="font-medium">自动记忆</h3>
      <p class="text-[13px] leading-5 text-muted-foreground">Claude 在此项目中工作时自行记录的笔记：做过的决定、约定、你的偏好、踩过的坑。每个新会话开始时读取 MEMORY.md，需要时再读各个记忆文件。这些文件放在共享文件夹的 .memory/ 里，员工可以直接修改；跨项目的偏好在 设置 → 我的记忆。</p>
    </div>
    <MemoryFileList :key="project.id" :pid="project.id" :label="`${project.name} 项目`" heading="阅读所有讨论串" />
  </div>
</template>
