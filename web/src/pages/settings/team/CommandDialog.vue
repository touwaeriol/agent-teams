<script setup lang="ts">
// 添加 / 编辑命令: name, description, mode (prompt template, or passthrough to Claude Code when
// the role can run slash commands) and the template with {{args}}.
import { computed, ref, watch } from "vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import { cleanCommand, commandProblem, type CustomCommand } from "@/lib/commands";

const props = defineProps<{ command: CustomCommand | null; taken: string[]; promptOnly: boolean; codex: boolean }>();
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ save: [command: CustomCommand] }>();
const blank = (): CustomCommand => ({ name: "", description: "", mode: "prompt", prompt: "" });
const d = ref<CustomCommand>(blank());
watch(open, (o) => o && (d.value = props.command ? { ...blank(), ...props.command } : blank()));
const problem = computed(() => commandProblem(d.value, props.taken, props.promptOnly));
const touched = computed(() => d.value.name.trim() !== "");
</script>

<template>
  <FormDialog v-model:open="open" :title="command ? `编辑 /${command.name}` : '添加命令'" submit-label="确定" width="36rem" :disabled="Boolean(problem)" @submit="emit('save', cleanCommand(d))">
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="名称" hint="输入 /名称 使用">
        <el-input :id="id" v-model="d.name" class="at-mono" placeholder="plan">
          <template #prefix>/</template>
        </el-input>
      </Field>
      <Field v-slot="{ id }" label="说明"><el-input :id="id" v-model="d.description" placeholder="在 / 列表里显示" /></Field>
    </div>
    <Field label="方式">
      <el-radio-group v-model="d.mode" aria-label="方式">
        <el-radio value="prompt">提示词模板</el-radio>
        <el-radio v-if="!promptOnly" value="passthrough">透传给 Claude Code</el-radio>
      </el-radio-group>
      <template #hint>
        <template v-if="d.mode === 'passthrough'">把 “/名称 后面的文字” 原样发给会话，由 Claude Code 自己运行：适合技能、插件命令和无界面可用的内置命令（如 /compact）。</template>
        <template v-else-if="codex">Codex 没有斜杠命令，只能用提示词模板。</template>
        <template v-else-if="promptOnly">成员收到的是平台转达的消息，只能用提示词模板。</template>
        <template v-else>平台把模板展开成一段指令再发给员工。</template>
      </template>
    </Field>
    <Field v-if="d.mode !== 'passthrough'" v-slot="{ id }" label="提示词模板" hint="{{args}} 会换成命令后面的文字；模板里没有 {{args}} 时，文字附在模板后面。">
      <el-input :id="id" v-model="d.prompt" type="textarea" :rows="5" placeholder="先列出计划再动手，目标：{{args}}" />
    </Field>
    <p v-if="touched && problem" class="text-xs text-destructive" role="alert">{{ problem }}</p>
  </FormDialog>
</template>
