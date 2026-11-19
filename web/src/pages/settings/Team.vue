<script setup lang="ts">
// 团队岗位: role templates (coordinator → lead → members) edited as one draft and saved together,
// presets, a YAML view, then the skill and plugin libraries. Each role names its model by hand.
import { computed, ref, watch } from "vue";
import { roles as roleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { useQueryClient } from "@tanstack/vue-query";
import { conversationModels, keys, useProjects, useRoles } from "@/api/queries";
import type { RoleView } from "@/api/views";
import { useOfficeLayout } from "@/components/app/avatar/layout";
import PluginLibrary from "@/components/app/library/PluginLibrary.vue";
import SkillLibrary from "@/components/app/library/SkillLibrary.vue";
import Callout from "@/components/common/Callout.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Loading from "@/components/common/Loading.vue";
import Spinner from "@/components/common/Spinner.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { fromPreset, LEVEL, newRole, roleProblems, sortRoles } from "@/lib/roles";
import PresetMenu from "./team/PresetMenu.vue";
import RoleCard from "./team/RoleCard.vue";
import { addEmployee, headcountChanges, saveImpact } from "./team/staff";
import StaffPanel from "./team/StaffPanel.vue";
import { isDirty, ordered, patchAt, removeAt } from "./team/team";
import YamlEditor from "./team/YamlEditor.vue";

const roles = useRoles();
const yaml = ref(false);
const draft = ref<RoleView[] | null>(null);
const saved = computed(() => sortRoles(roles.data.value?.roles ?? []));
watch(() => roles.data.value, (d) => d && (draft.value = sortRoles(d.roles ?? [])), { immediate: true });
const client = useQueryClient();
// Saving also brings the coordinators' and the open threads' leads' models in line: refresh what the chat boxes show.
const save = useAction(() => roleActions.save(ordered(draft.value ?? [])), {
  invalidate: [keys.roles],
  success: "岗位模板已保存",
  onSuccess: () => void client.invalidateQueries(conversationModels),
});
const layout = useOfficeLayout();
const dirty = computed(() => draft.value !== null && isDirty(draft.value, saved.value));
const problems = computed(() => roleProblems(draft.value ?? []));
const warnings = computed(() => roles.data.value?.warnings ?? []);
const list = () => draft.value ?? [];
// Changing 编制 re-staffs every project: ask first.
const projects = useProjects();
const staffChanges = computed(() => headcountChanges(list(), saved.value));
const confirmStaff = ref(false);
const onSave = () => (staffChanges.value.length ? (confirmStaff.value = true) : save.mutate());

const setRole = (i: number, patch: Partial<RoleView>) => (draft.value = patchAt(list(), i, patch));
const removeRole = (i: number) => (draft.value = removeAt(list(), i));
const addPreset = (p: RoleView) => (draft.value = [...list(), fromPreset(list(), p)]);
const addStaff = (role: string) => (draft.value = addEmployee(list(), role));
function add() {
  const colors = layout.value.looks.colors;
  draft.value = [...list(), newRole(list(), colors[list().length % colors.length] ?? "#4C7BD0")];
}
const PILL = "rounded-full px-2.5 py-1";
</script>

<template>
  <Loading v-if="!draft" class="h-full" />
  <main v-else class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[1040px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">团队岗位</h1>
            <p class="max-w-[640px] text-[13px] leading-[22px] text-muted-foreground">每个项目有一个项目统筹；每开一个讨论串配一个组长；成员按下面的岗位人数常驻每个项目，所有讨论串共用。</p>
          </div>
        </div>
        <div class="flex gap-2">
          <el-button text class="h-[38px]" @click="yaml = !yaml">{{ yaml ? "卡片视图" : "YAML" }}</el-button>
          <template v-if="!yaml">
            <PresetMenu @pick="addPreset" />
            <el-button class="!ml-0 h-[38px]" @click="add">添加岗位</el-button>
            <el-button type="primary" class="!ml-0 h-[38px]" :disabled="!dirty || problems.length > 0 || save.isPending.value" @click="onSave">
              <Spinner v-if="save.isPending.value" /> 保存
            </el-button>
          </template>
        </div>
      </header>
      <section class="flex flex-wrap items-center gap-2 rounded-xl border bg-panel px-3.5 py-3 text-[13px]">
        <span class="font-semibold">层级</span>
        <span :class="PILL" :style="{ background: LEVEL.coordinator.bg, color: LEVEL.coordinator.fg }">项目统筹 · 每项目 1 个</span>
        <span class="text-[#8A877F]">→</span>
        <span :class="PILL" :style="{ background: LEVEL.lead.bg, color: LEVEL.lead.fg }">组长 · 每讨论串 1 个</span>
        <span class="text-[#8A877F]">→</span>
        <span :class="PILL" :style="{ background: LEVEL.member.bg, color: LEVEL.member.fg }">成员 · 每项目按岗位编制</span>
        <span class="basis-full text-xs text-muted-foreground">成员是项目的固定编制：岗位人数就是每个项目配几个，所有讨论串共用、轮流领任务队列里的活；人手不够就加人数。</span>
      </section>
      <Callout v-for="w in warnings" :key="`${w.role}-${w.message}`" tone="warn">{{ w.role ? `${w.role}：` : "" }}{{ w.message }}</Callout>
      <Callout v-if="dirty && problems.length > 0" tone="warn">{{ problems.join("；") }}</Callout>
      <StaffPanel v-if="!yaml" :roles="draft" :saved="saved" @add="addStaff" />
      <ConfirmAction
        v-model:open="confirmStaff"
        title="改成员编制"
        :description="saveImpact(staffChanges, (projects.data.value ?? []).length)"
        confirm-label="保存"
        @confirm="save.mutate()"
      />
      <YamlEditor v-if="yaml" />
      <div v-else class="grid gap-3.5 lg:grid-cols-2">
        <RoleCard v-for="(r, i) in draft" :key="r.id || `new-${i}`" :role="r" :roles="draft" :layout="layout" @change="(p) => setRole(i, p)" @remove="removeRole(i)" />
      </div>
      <SkillLibrary v-if="!yaml" />
      <PluginLibrary v-if="!yaml" />
    </div>
  </main>
</template>
