<script setup lang="ts">
// One role template: name and tier, count, menu (编辑详情 / 删除岗位), runtime, model (every tier,
// typed by hand; "" = the project setting, then the built-in default) and nodes, the rule line,
// look, skills, plugins and (members) permission mode. Emits patches; the page keeps the draft.
import { MoreHorizontal } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { RoleView } from "@/api/views";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import type { OfficeLayout } from "@/office";
import { canHaveSkills, countText, isReadOnly, LEVEL, readOnlyCaveat, roleName, ruleText } from "@/lib/roles";
import { modelOf, modelPatch, roleRuntimes, runtimeLabel, runtimesFor, runtimesPatch, type Runtime } from "@/lib/runtimes";
import { toast } from "@/lib/toast";
import CommandEditor from "./CommandEditor.vue";
import LookEditor from "./LookEditor.vue";
import NodePicker from "./NodePicker.vue";
import PermissionField from "./PermissionField.vue";
import PluginPicker from "./PluginPicker.vue";
import RoleDialog from "./RoleDialog.vue";
import RoleModelSelect from "./RoleModelSelect.vue";
import SkillPicker from "./SkillPicker.vue";

const props = defineProps<{ role: RoleView; layout: OfficeLayout; roles?: RoleView[] }>();
const emit = defineEmits<{ change: [patch: Partial<RoleView>]; remove: [] }>();
const level = computed(() => LEVEL[props.role.tier]);
const editing = ref(false);
const COMMANDS: Record<string, () => void> = { edit: () => (editing.value = true), remove: () => emit("remove") };
const member = computed(() => props.role.tier === "member");
const runtimeList = computed(() => roleRuntimes(props.role));
/** Members only (the others run on Claude Code): several agents, each new session takes the one with the most quota left; each keeps its own model. */
function setRuntimes(list: Runtime[]) {
  if (!member.value) return;
  const patch = runtimesPatch(props.role, list);
  if (!patch) return toast.info("至少选一种 agent");
  emit("change", patch);
}
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
            <el-dropdown-item v-if="member" command="remove" class="is-destructive">删除岗位</el-dropdown-item>
          </template>
        </DropdownMenu>
      </span>
    </div>
    <div class="grid grid-cols-1 gap-2.5 text-[13px] min-[480px]:grid-cols-[8rem_minmax(0,1.25fr)_minmax(0,1fr)]">
      <span class="flex min-w-0 flex-col gap-1">
        <span class="text-xs text-muted-foreground">agent</span>
        <el-select :model-value="runtimeList" multiple :disabled="!member" class="at-select-36 at-text-13" aria-label="agent" @update:model-value="setRuntimes">
          <el-option v-for="r in runtimesFor(role.tier)" :key="r.id" :value="r.id" :label="r.label" />
        </el-select>
        <span v-if="runtimeList.length > 1" class="text-[11px] leading-4 text-muted-foreground">每次开新会话用剩余额度最多的</span>
      </span>
      <span class="flex min-w-0 flex-col gap-1">
        <span class="text-xs text-muted-foreground">模型</span>
        <span v-for="rt in runtimeList" :key="rt" class="flex min-w-0 flex-col gap-0.5">
          <span v-if="runtimeList.length > 1" class="text-[11px] text-muted-foreground">{{ runtimeLabel(rt) }}</span>
          <RoleModelSelect :runtime="rt" :roles="roles" :model-value="modelOf(role, rt)" @update:model-value="(model: string) => emit('change', modelPatch(role, rt, model))" />
        </span>
      </span>
      <span class="flex min-w-0 flex-col gap-1">
        <span class="text-xs text-muted-foreground">可用节点</span>
        <NodePicker :model-value="role.node_ids ?? []" @update:model-value="(ids: string[]) => emit('change', { node_ids: ids })" />
      </span>
    </div>
    <span class="rounded-lg bg-background px-2.5 py-2 text-xs leading-[19px] text-[#4A4843]">{{ ruleText(role) }}</span>
    <span v-if="readOnlyCaveat(role)" class="rounded-lg bg-[hsl(var(--warn)/0.12)] px-2.5 py-2 text-xs leading-[19px] text-[hsl(var(--warn))]" data-readonly-caveat>{{ readOnlyCaveat(role) }}</span>
    <LookEditor :role="role" :layout="layout" @change="(look) => emit('change', { look })" />
    <SkillPicker v-if="canHaveSkills(role)" :model-value="role.skills ?? []" @update:model-value="(skills: string[]) => emit('change', { skills })" />
    <PluginPicker v-if="canHaveSkills(role)" :role="role" @change="(plugins: string[]) => emit('change', { plugins })" />
    <PermissionField v-if="member && !isReadOnly(role)" :role="role" @change="(p) => emit('change', p)" />
    <CommandEditor :role="role" @change="(p) => emit('change', p)" />
    <RoleDialog v-model:open="editing" :role="role" @save="save" />
  </article>
</template>
