<script setup lang="ts">
// 新建项目: name, 项目指令 (context), repositories, work mode, the per-thread team from the role templates,
// the project's models (跟随岗位配置 unless set per type) and an optional first message for the coordinator.
import { computed, ref } from "vue";
import { RouterLink, useRouter } from "vue-router";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useModels, useRoles } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import ChoiceCards from "@/components/app/ChoiceCards.vue";
import RepoList from "@/components/app/repo/RepoList.vue";
import { reposPayload, type RepoRef } from "@/components/app/repo/repo";
import { teamChips } from "@/components/app/roles";
import { WORK_MODES } from "@/components/app/workModes";
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import Field from "@/components/common/Field.vue";
import Spinner from "@/components/common/Spinner.vue";
import { offersCodex, projectModelStatus, type ProjectModelScope, type Runtime } from "@/lib/models";
import { INSTRUCTIONS_MAX } from "@/lib/projectSettings";
import NavButton from "@/components/shell/NavButton.vue";

const router = useRouter();
const roles = useRoles();
const name = ref("");
const context = ref("");
const repos = ref<RepoRef[]>([]);
const mode = ref<"worktree" | "clone">("worktree");
const first = ref("");
const models = useModels();
/** The project's models; "" = 跟随岗位配置. */
const picked = ref({ coordinator_model: "", thread_model: "", thread_codex_model: "" });
const pickModels = ref(false);
const MODEL_FIELDS: { key: keyof typeof picked.value; scope: ProjectModelScope; runtime: Runtime; label: string }[] = [
  { key: "coordinator_model", scope: "coordinator", runtime: "claude", label: "统筹 · Claude Code" },
  { key: "thread_model", scope: "claude", runtime: "claude", label: "讨论串 · Claude Code" },
  { key: "thread_codex_model", scope: "codex", runtime: "codex", label: "讨论串 · Codex" },
];
const roleList = computed(() => roles.data.value?.roles ?? []);
const modelList = computed(() => models.data.value?.models ?? []);
const modelFields = computed(() => MODEL_FIELDS.filter((f) => f.scope !== "codex" || offersCodex(roleList.value, modelList.value, picked.value.thread_codex_model)));
const statusOf = (f: (typeof MODEL_FIELDS)[number]) => projectModelStatus(picked.value[f.key], roleList.value, modelList.value, f.scope);
function followRoles() {
  picked.value = { coordinator_model: "", thread_model: "", thread_codex_model: "" };
  pickModels.value = false;
}
const create = useAction(
  () =>
    projects.create({
      name: name.value.trim(),
      context: context.value,
      work_mode: mode.value,
      first_message: first.value.trim() || undefined,
      repos: reposPayload(repos.value),
      ...picked.value,
    }),
  { invalidate: [keys.projects], success: (p) => `已创建项目「${p.name}」`, onSuccess: (p) => void router.push(`/projects/${p.id}`) },
);
const submit = () => name.value.trim() && create.mutate();
const chips = computed(() => teamChips(roles.data.value?.roles ?? []));
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-10">
    <form class="mx-auto flex max-w-[760px] flex-col gap-6" @submit.prevent="submit">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">新建项目</h1>
          <p class="text-[13px] leading-[22px] text-muted-foreground">项目是入口：你和项目统筹聊需求，统筹开讨论串，组长带成员在仓库里完成。</p>
        </div>
      </div>
      <Card class="gap-4">
        <Field v-slot="{ id }" label="项目名称">
          <el-input :id="id" v-model="name" required placeholder="例如：agent team" />
        </Field>
        <Field v-slot="{ id }" label="项目指令" hint="类似 CLAUDE.md：统筹、组长和每个成员的每个新会话都会读取并遵循；各仓库的基准分支也写在这里。之后在 设置 → 记忆 里修改。">
          <el-input :id="id" v-model="context" type="textarea" :rows="4" :maxlength="INSTRUCTIONS_MAX" placeholder="技术栈、约定、怎么跑测试、不能动的地方、仓库 org/web 的基准分支：develop……" />
        </Field>
      </Card>
      <Card class="gap-4">
        <CardTitle>代码仓库</CardTitle>
        <RepoList v-model="repos" />
      </Card>
      <Card class="gap-3.5">
        <CardTitle>工作方式</CardTitle>
        <ChoiceCards v-model="mode" label="工作方式" :options="WORK_MODES" />
      </Card>
      <Card class="gap-3">
        <CardTitle>
          项目团队
          <template #action><RouterLink to="/settings/team" class="text-[13px] underline-offset-2 hover:underline">编辑岗位模板</RouterLink></template>
        </CardTitle>
        <p class="text-[13px] text-muted-foreground">项目按这里的岗位编制配成员，所有讨论串共用；每个讨论串只多一个组长。节点槽位不够时排队。</p>
        <div class="flex flex-wrap gap-2">
          <span v-for="r in chips" :key="r.name" class="flex h-[34px] items-center gap-2 rounded-lg border bg-background px-3 text-[13px]">
            {{ r.name }}
            <span class="text-muted-foreground">× {{ r.n }}</span>
            <span class="text-[11px] text-muted-foreground">{{ r.kind }}</span>
          </span>
        </div>
      </Card>
      <Card class="gap-3" data-testid="new-project-models">
        <CardTitle>
          模型
          <template #action>
            <button v-if="pickModels" type="button" class="text-[13px] underline-offset-2 hover:underline" @click="followRoles">全部跟随岗位配置</button>
            <button v-else type="button" class="text-[13px] underline-offset-2 hover:underline" @click="pickModels = true">按类型指定</button>
          </template>
        </CardTitle>
        <p class="text-[13px] text-muted-foreground">默认跟随岗位配置（每个岗位用自己的模型）。按类型指定后，覆盖这个项目里同类型岗位的模型（讨论串 = 组长和成员）；之后可在项目设置里修改。</p>
        <div v-if="pickModels" class="grid gap-3 sm:grid-cols-[repeat(auto-fit,minmax(200px,1fr))]">
          <Field v-for="f in modelFields" :key="f.key" v-slot="{ id }" :label="f.label" :hint="statusOf(f)">
            <ModelPicker :id="id" v-model="picked[f.key]" :runtime="f.runtime" :label="f.label" empty-label="跟随岗位配置" class="at-select-36" />
          </Field>
        </div>
      </Card>
      <Field v-slot="{ id }" label="第一件事（可选，直接发给项目统筹）">
        <el-input :id="id" v-model="first" type="textarea" :rows="3" placeholder="例如：给项目加上置顶、归档、删除" class="at-input-white" />
      </Field>
      <div class="flex justify-end gap-2">
        <el-button size="large" @click="router.push('/projects')">取消</el-button>
        <el-button type="primary" size="large" native-type="submit" :disabled="!name.trim() || create.isPending.value">
          <Spinner v-if="create.isPending.value" /> 创建项目
        </el-button>
      </div>
    </form>
  </main>
</template>
