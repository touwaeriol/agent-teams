<script setup lang="ts">
/**
 * 模型: the project's member models per type (Claude Code / Codex), each "" = 跟随岗位配置; a set
 * model overrides the member roles of that type in this project. The coordinator and the leads
 * always use the owner's default, so they have no row. Each row says what is in effect. The
 * threads' effort sits with the Claude Code row.
 */
import { computed } from "vue";
import { useModels, useRoles } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import { offersCodex, projectModelStatus } from "@/lib/models";
import { EFFORTS } from "@/lib/projectSettings";
import { fromChoice, toChoice } from "./logic";
import ModelSelect from "./ModelSelect.vue";
import Row from "./Row.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
const models = useModels();
const roles = useRoles();
const list = computed(() => models.data.value?.models ?? []);
const roleList = computed(() => roles.data.value?.roles ?? []);
const codexValue = computed(() => props.project.thread_codex_model ?? "");
const showCodex = computed(() => offersCodex(roleList.value, list.value, codexValue.value));
const status = {
  claude: computed(() => projectModelStatus(props.project.thread_model, roleList.value, list.value, "claude")),
  codex: computed(() => projectModelStatus(codexValue.value, roleList.value, list.value, "codex")),
};
const WIDE = "!w-[min(256px,50vw)]";
</script>

<template>
  <div class="flex flex-col pt-3">
    <span class="pb-1">模型</span>
    <span class="text-[13px] text-muted-foreground">
      只对成员岗位生效：默认跟随岗位配置，为项目指定模型后覆盖这个项目里同类型成员岗位的模型。统筹和组长固定使用「我的默认」（没设时用内置默认），不受这里影响。改动对之后打开的会话生效。
    </span>
    <Row title="讨论串 · Claude Code" :desc="`成员岗位 · ${status.claude.value}`" data-testid="model-row-claude">
      <ModelSelect label="成员 Claude Code 模型" runtime="claude" :value="project.thread_model" class="!w-[min(160px,32vw)]" @change="(v: string) => update.mutate({ thread_model: v })" />
      <el-select
        :model-value="toChoice(project.thread_effort)"
        aria-label="讨论串思考强度"
        class="at-select-36 at-raised !w-[104px]"
        @update:model-value="(v: string) => update.mutate({ thread_effort: fromChoice(v) })"
      >
        <el-option v-for="e in EFFORTS" :key="toChoice(e.value)" :value="toChoice(e.value)" :label="e.value ? e.label : '强度跟随岗位'" />
      </el-select>
    </Row>
    <Row v-if="showCodex" title="讨论串 · Codex" :desc="`成员岗位 · ${status.codex.value}`" data-testid="model-row-codex">
      <ModelSelect label="成员 Codex 模型" runtime="codex" :value="codexValue" :class="WIDE" @change="(v: string) => update.mutate({ thread_codex_model: v })" />
    </Row>
  </div>
</template>
