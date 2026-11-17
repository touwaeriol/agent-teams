<script setup lang="ts">
// One role template: name and tier, count, menu (编辑详情 / 删除岗位), runtime and nodes, the rule
// line, look, skills, plugins and (members) permission mode. Emits patches; the page keeps the draft.
import { MoreHorizontal } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { RoleView } from "@/api/views";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import type { OfficeLayout } from "@/office";
import { canHaveSkills, countText, isReadOnly, kindText, LEVEL, roleName, ruleText } from "@/lib/roles";
import CommandEditor from "./CommandEditor.vue";
import LookEditor from "./LookEditor.vue";
import NodePicker from "./NodePicker.vue";
import PermissionField from "./PermissionField.vue";
import PluginPicker from "./PluginPicker.vue";
import RoleDialog from "./RoleDialog.vue";
import SkillPicker from "./SkillPicker.vue";

const props = defineProps<{ role: RoleView; layout: OfficeLayout }>();
const emit = defineEmits<{ change: [patch: Partial<RoleView>]; remove: [] }>();
const level = computed(() => LEVEL[props.role.tier]);
const editing = ref(false);
const COMMANDS: Record<string, () => void> = { edit: () => (editing.value = true), remove: () => emit("remove") };
function save(r: RoleView) {
  emit("change", r);
  editing.value = false;
}
</script>

<template>
  <article class="flex flex-col gap-3.5 rounded-[14px] border bg-panel p-[18px]">
    <div class="flex items-start justify-between gap-2.5">
      <div class="flex flex-col gap-1">
        <span class="flex items-center gap-2">
          <span class="text-base font-semibold">{{ roleName(role) }}</span>
          <span class="rounded-full px-2 py-0.5 text-[11px]" :style="{ background: level.bg, color: level.fg }">{{ level.label }}</span>
        </span>
        <span class="text-[13px] leading-[21px] text-[#4A4843]">{{ role.description || "还没有职责说明。" }}</span>
      </div>
      <span class="flex items-center gap-1">
        <span class="whitespace-nowrap text-xs text-muted-foreground">{{ countText(role) }}</span>
        <DropdownMenu placement="bottom-end" @command="(c: string) => COMMANDS[c]?.()">
          <button type="button" :aria-label="`${roleName(role)} 的菜单`" class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-raised">
            <MoreHorizontal class="size-4" />
          </button>
          <template #menu>
            <el-dropdown-item command="edit">编辑详情</el-dropdown-item>
            <el-dropdown-item v-if="role.tier === 'member'" command="remove" class="is-destructive">删除岗位</el-dropdown-item>
          </template>
        </DropdownMenu>
      </span>
    </div>
    <div class="grid grid-cols-2 gap-2.5 text-[13px]">
      <span class="flex flex-col gap-1">
        <span class="text-xs text-muted-foreground">类型</span>
        <el-select :model-value="role.runtime" :disabled="role.tier !== 'member'" class="at-select-36 at-text-13" aria-label="类型" @update:model-value="(v: RoleView['runtime']) => emit('change', { runtime: v })">
          <template #label>{{ kindText(role) }}</template>
          <el-option value="claude" label="Claude Code" />
          <el-option value="codex" label="Codex" />
        </el-select>
      </span>
      <span class="flex flex-col gap-1">
        <span class="text-xs text-muted-foreground">可用节点</span>
        <NodePicker :model-value="role.node_ids ?? []" @update:model-value="(ids: string[]) => emit('change', { node_ids: ids })" />
      </span>
    </div>
    <span class="rounded-lg bg-background px-2.5 py-2 text-xs leading-[19px] text-[#4A4843]">{{ ruleText(role) }}</span>
    <LookEditor :role="role" :layout="layout" @change="(look) => emit('change', { look })" />
    <SkillPicker v-if="canHaveSkills(role)" :model-value="role.skills ?? []" @update:model-value="(skills: string[]) => emit('change', { skills })" />
    <PluginPicker v-if="canHaveSkills(role)" :role="role" @change="(plugins: string[]) => emit('change', { plugins })" />
    <PermissionField v-if="role.tier === 'member' && !isReadOnly(role)" :role="role" @change="(p) => emit('change', p)" />
    <CommandEditor :role="role" @change="(p) => emit('change', p)" />
    <RoleDialog v-model:open="editing" :role="role" @save="save" />
  </article>
</template>
