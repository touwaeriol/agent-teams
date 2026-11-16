<script setup lang="ts">
/** 模型: the coordinator's model, and the threads' model and effort ("follow the role" by default). */
import type { ProjectView } from "@/api/views";
import { EFFORTS } from "@/lib/projectSettings";
import { fromChoice, toChoice } from "./logic";
import ModelSelect from "./ModelSelect.vue";
import Row from "./Row.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
</script>

<template>
  <div class="flex flex-col pt-3">
    <span class="pb-1">模型</span>
    <span class="text-[13px] text-muted-foreground">跟随岗位设置时用岗位模板里的模型；改动对之后打开的会话生效。</span>
    <Row title="协调者">
      <ModelSelect label="协调者模型" :value="project.coordinator_model" class="!w-[min(256px,50vw)]" @change="(v: string) => update.mutate({ coordinator_model: v })" />
    </Row>
    <Row title="讨论串" desc="组长和成员">
      <ModelSelect label="讨论串模型" :value="project.thread_model" class="!w-[min(160px,32vw)]" @change="(v: string) => update.mutate({ thread_model: v })" />
      <el-select
        :model-value="toChoice(project.thread_effort)"
        aria-label="讨论串思考强度"
        class="at-select-36 at-raised !w-[104px]"
        @update:model-value="(v: string) => update.mutate({ thread_effort: fromChoice(v) })"
      >
        <el-option v-for="e in EFFORTS" :key="toChoice(e.value)" :value="toChoice(e.value)" :label="e.value ? e.label : '强度跟随岗位'" />
      </el-select>
    </Row>
  </div>
</template>
