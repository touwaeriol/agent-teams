<script setup lang="ts">
/** 您想做些什么？ — the project context, saved with its button; counts to 8,000. */
import { computed, ref, watch } from "vue";
import type { ProjectView } from "@/api/views";
import { goalLimit, GOAL_MAX } from "@/lib/projectSettings";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const text = ref(props.project.context);
watch(() => props.project.context, (v) => (text.value = v));
const update = useUpdate(() => props.project.id, "已保存");
const limit = computed(() => goalLimit(props.project.context));
const dirty = computed(() => text.value !== props.project.context);
</script>

<template>
  <div class="flex flex-col gap-2 py-2.5">
    <label for="project-goal">您想做些什么？</label>
    <el-input
      id="project-goal"
      v-model="text"
      type="textarea"
      :rows="4"
      :maxlength="limit"
      class="at-raised"
      placeholder="例如：重新设计新手引导流程，让更多新用户完成设置。统筹、组长和每个成员开工前都会读到这里；各仓库的基准分支、分支约定也写在这里。"
    />
    <span class="flex items-center justify-end gap-3 text-[13px] text-muted-foreground">
      <template v-if="dirty">
        <el-button size="small" text @click="text = project.context">取消</el-button>
        <el-button size="small" type="primary" class="!ml-0" :disabled="update.isPending.value" @click="update.mutate({ context: text })">保存</el-button>
      </template>
      <span class="tabular-nums">{{ text.length.toLocaleString() }} / {{ GOAL_MAX.toLocaleString() }}</span>
    </span>
  </div>
</template>
