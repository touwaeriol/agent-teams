<script setup lang="ts">
// 编辑详情: the role template's other fields (type, model and nodes are on the card). Edits a copy taken when the dialog opens; 确定 hands
// the copy back (the page saves all roles together).
import { computed, ref, watch } from "vue";
import type { RoleView } from "@/api/views";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import TagInput from "@/components/common/TagInput.vue";
import { roleName } from "@/lib/roles";
import ContextFields from "./ContextFields.vue";
import { GATES } from "./team";

const props = defineProps<{ role: RoleView }>();
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ save: [role: RoleView] }>();
const copy = () => JSON.parse(JSON.stringify(props.role)) as RoleView;
const d = ref<RoleView>(copy());
watch(open, (o) => o && (d.value = copy()));
const member = computed(() => d.value.tier === "member");
const readOnly = computed(() => d.value.read_only ?? (d.value.gate === "review" || d.value.gate === "decision"));
const num = (v: string) => (v === "" ? null : Number(v));
</script>

<template>
  <FormDialog v-model:open="open" :title="`编辑 ${roleName(role)}`" submit-label="确定" width="42rem" class="max-h-[90vh] max-w-2xl overflow-y-auto" @submit="emit('save', d)">
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="名称"><el-input :id="id" v-model="d.title" /></Field>
      <Field v-slot="{ id }" label="岗位键" hint="小写字母、数字和 -，成员编号用它（dev-1）">
        <el-input :id="id" :model-value="d.role" :disabled="!member" class="at-mono" @update:model-value="(v: string) => (d.role = v.trim())" />
      </Field>
      <Field v-if="member" v-slot="{ id }" label="项目编制（人数）" hint="每个项目按这个人数配成员，所有讨论串共用">
        <el-input :id="id" type="number" :min="1" :max="8" :model-value="String(d.count)" @update:model-value="(v: string) => (d.count = Number(v))" />
      </Field>
      <Field v-slot="{ id }" label="思考强度（effort）">
        <el-input :id="id" :model-value="d.effort" placeholder="例如 high" @update:model-value="(v: string) => (d.effort = v.trim())" />
      </Field>
      <Field v-slot="{ id }" label="最多回合数">
        <el-input :id="id" type="number" :min="1" :model-value="d.max_turns == null ? '' : String(d.max_turns)" @update:model-value="(v: string) => (d.max_turns = num(v))" />
      </Field>
    </div>
    <Field v-slot="{ id }" label="职责说明"><el-input :id="id" v-model="d.description" type="textarea" :rows="2" /></Field>
    <Field v-slot="{ id }" label="指令" hint="开工时写进系统提示"><el-input :id="id" v-model="d.instructions" type="textarea" :rows="4" /></Field>
    <ContextFields v-model="d" />
    <div v-if="member" class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="把关">
        <el-select :id="id" :model-value="d.gate || 'none'" @update:model-value="(v: string) => (d.gate = v === 'none' ? '' : v)">
          <el-option v-for="g in GATES" :key="g.value" :value="g.value" :label="g.label" />
        </el-select>
      </Field>
      <Field v-if="d.runtime === 'codex'" v-slot="{ id }" label="Codex 沙箱">
        <el-select :id="id" :model-value="d.sandbox || 'workspace-write'" @update:model-value="(v: string) => (d.sandbox = v)">
          <el-option v-for="s in ['read-only', 'workspace-write', 'danger-full-access']" :key="s" :value="s" :label="s" />
        </el-select>
      </Field>
      <Field v-slot="{ id }" label="可领的任务类型" hint="回车添加，例如 dev、test">
        <TagInput :id="id" :model-value="d.task_kinds ?? []" @update:model-value="(v: string[]) => (d.task_kinds = v)" />
      </Field>
      <Field v-slot="{ id }" label="允许的工具">
        <TagInput :id="id" :model-value="d.allowed_tools ?? []" placeholder="Bash(git *)" @update:model-value="(v: string[]) => (d.allowed_tools = v)" />
      </Field>
      <Field v-slot="{ id }" label="禁用的工具">
        <TagInput :id="id" :model-value="d.disallowed_tools ?? []" @update:model-value="(v: string[]) => (d.disallowed_tools = v)" />
      </Field>
      <label class="flex items-center justify-between gap-3 self-end rounded-lg border px-3 py-2 text-[13px]">
        只读（不改代码）
        <el-switch :model-value="readOnly" @update:model-value="(v: string | number | boolean) => (d.read_only = Boolean(v))" />
      </label>
      <label class="flex items-center justify-between gap-3 self-end rounded-lg border px-3 py-2 text-[13px]">
        可以给别人派活
        <el-switch v-model="d.can_delegate" />
      </label>
    </div>
  </FormDialog>
</template>
