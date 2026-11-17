<script setup lang="ts">
/** 项目指令: the project's `context`, written by the owner and read by every session (like CLAUDE.md). */
import { computed, ref, watch } from "vue";
import type { ProjectView } from "@/api/views";
import { INSTRUCTIONS_MAX } from "@/lib/projectSettings";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const text = ref(props.project.context);
watch(() => props.project.context, (v) => (text.value = v));
const update = useUpdate(() => props.project.id, "项目指令已保存，新开的会话会读到");
const dirty = computed(() => text.value !== props.project.context);
const over = computed(() => text.value.length > INSTRUCTIONS_MAX);
</script>

<template>
  <div class="flex flex-col gap-2 pb-2">
    <span class="flex flex-col gap-0.5">
      <label for="project-instructions" class="font-medium">项目指令</label>
      <span class="break-words text-[13px] leading-5 text-muted-foreground">类似 CLAUDE.md：由您编写的指令和规则，统筹、组长和每个成员的每个新会话都会读取并遵循。各仓库的基准分支、分支约定也写在这里。</span>
    </span>
    <el-input
      id="project-instructions"
      v-model="text"
      type="textarea"
      :rows="6"
      class="at-raised w-full"
      placeholder="例如：回答用中文；改完前端要跑 tsc 和 vite build；仓库 org/web 的基准分支：develop。"
    />
    <span class="flex flex-wrap items-center justify-end gap-3 text-[13px] text-muted-foreground">
      <template v-if="dirty">
        <el-button size="small" text @click="text = project.context">取消</el-button>
        <el-button size="small" type="primary" class="!ml-0" :disabled="over || update.isPending.value" @click="update.mutate({ context: text })">保存</el-button>
      </template>
      <span :class="['tabular-nums', over && 'text-destructive']">{{ text.length.toLocaleString() }} / {{ INSTRUCTIONS_MAX.toLocaleString() }}</span>
    </span>
  </div>
</template>
