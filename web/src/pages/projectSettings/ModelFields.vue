<script setup lang="ts">
/**
 * 模型: the project's models — the coordinator's, and the threads' per type (Claude Code / Codex,
 * leads and members) — each "" = 跟随岗位配置; a set model overrides the roles it covers in this
 * project (members' /model still wins for them). Each row says what is in effect. The threads'
 * effort sits with the Claude Code row.
 */
import { computed } from "vue";
import { useRoles } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import { offersCodex, projectModelStatus } from "@/lib/models";
import { EFFORTS } from "@/lib/projectSettings";
import { fromChoice, toChoice } from "./logic";
import ModelSelect from "./ModelSelect.vue";
import Row from "./Row.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
const roles = useRoles();
const roleList = computed(() => roles.data.value?.roles ?? []);
const coordValue = computed(() => props.project.coordinator_model ?? "");
const codexValue = computed(() => props.project.thread_codex_model ?? "");
const showCodex = computed(() => offersCodex(roleList.value, codexValue.value));
const status = {
  coordinator: computed(() => projectModelStatus(coordValue.value, roleList.value, "coordinator")),
  claude: computed(() => projectModelStatus(props.project.thread_model, roleList.value, "claude")),
  codex: computed(() => projectModelStatus(codexValue.value, roleList.value, "codex")),
};
const WIDE = "!w-[min(256px,50vw)]";
</script>

<template>
  <div class="flex flex-col pt-3">
    <span class="pb-1">模型</span>
    <span class="text-[13px] text-muted-foreground">
      默认跟随岗位配置；为项目指定模型后，覆盖这个项目里对应岗位的模型（成员用 /model 选的仍然优先）。可以直接输入模型 ID。改动从下一轮或之后打开的会话生效。
    </span>
    <Row title="统筹" :desc="`统筹 · ${status.coordinator.value}`" data-testid="model-row-coordinator">
      <ModelSelect label="统筹模型" runtime="claude" :value="coordValue" :class="WIDE" @change="(v: string) => update.mutate({ coordinator_model: v })" />
    </Row>
    <Row title="讨论串 · Claude Code" :desc="`组长和成员 · ${status.claude.value}`" data-testid="model-row-claude">
      <ModelSelect label="讨论串 Claude Code 模型" runtime="claude" :value="project.thread_model" class="!w-[min(160px,32vw)]" @change="(v: string) => update.mutate({ thread_model: v })" />
      <el-select
        :model-value="toChoice(project.thread_effort)"
        aria-label="讨论串思考强度"
        class="at-select-36 at-raised !w-[104px]"
        @update:model-value="(v: string) => update.mutate({ thread_effort: fromChoice(v) })"
      >
        <el-option v-for="e in EFFORTS" :key="toChoice(e.value)" :value="toChoice(e.value)" :label="e.value ? e.label : '强度跟随岗位'" />
      </el-select>
    </Row>
    <Row v-if="showCodex" title="讨论串 · Codex" :desc="`Codex 岗位 · ${status.codex.value}`" data-testid="model-row-codex">
      <ModelSelect label="讨论串 Codex 模型" runtime="codex" :value="codexValue" :class="WIDE" @change="(v: string) => update.mutate({ thread_codex_model: v })" />
    </Row>
  </div>
</template>
