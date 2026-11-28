<script setup lang="ts">
// 工作流程: the user's flow charts (先开发、再测试、再审查…). Each step names the role that does it;
// a project's thread leads get the project's flow in their prompt and split tasks by it (a
// reference: the platform does not expand or block tasks). The default flow applies to projects
// that pick none (project settings → 高级).
import { Workflow as FlowIcon } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { workflows as flowActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useRoles, useWorkflows } from "@/api/queries";
import type { FlowDraft, FlowView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Field from "@/components/common/Field.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { cn } from "@/lib/utils";
import FlowEditor from "./workflows/FlowEditor.vue";
import { draftOf, freeName, looseSteps, presets, roleChoices, unlabeledBranches } from "./workflows/flow";

const list = useWorkflows();
const roles = useRoles();
const flows = computed(() => list.data.value?.workflows ?? []);
const choices = computed(() => roleChoices(roles.data.value?.roles ?? []));
const options = computed(() => presets(roles.data.value?.roles ?? []));

/** The flow being edited: a saved one (id) or a new one (id null). */
const current = ref<{ id: string | null; draft: FlowDraft } | null>(null);
const dirty = ref(false);

watch(
  flows,
  (fs) => {
    if (current.value || fs.length === 0) return;
    open(fs[0]!);
  },
  { immediate: true },
);

function open(f: FlowView) {
  current.value = { id: f.id, draft: draftOf(f) };
  dirty.value = false;
}

function startNew(draft: FlowDraft) {
  current.value = { id: null, draft: { ...(JSON.parse(JSON.stringify(draft)) as FlowDraft), name: freeName(draft.name, flows.value), is_default: flows.value.length === 0 } };
  dirty.value = true;
}

function edit(d: FlowDraft) {
  if (!current.value) return;
  current.value = { ...current.value, draft: d };
  dirty.value = true;
}

const invalidate = [keys.workflows];
const save = useAction(
  (c: { id: string | null; draft: FlowDraft }) => (c.id ? flowActions.update(c.id, c.draft) : flowActions.create(c.draft)),
  {
    invalidate,
    success: "流程已保存",
    onSuccess: (saved) => {
      current.value = { id: saved.id, draft: draftOf(saved) };
      dirty.value = false;
    },
  },
);
const remove = useAction((id: string) => flowActions.remove(id), {
  invalidate,
  success: "流程已删除",
  onSuccess: (_, id) => {
    current.value = null;
    const next = flows.value.find((f) => f.id !== id);
    if (next) open(next);
  },
});

const loose = computed(() => (current.value ? looseSteps(current.value.draft) : []));
const unlabeled = computed(() => (current.value ? unlabeledBranches(current.value.draft) : 0));
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[1200px] flex-col gap-5">
      <header class="flex flex-wrap items-start gap-3">
        <NavButton />
        <div class="flex min-w-0 flex-1 basis-80 flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">工作流程</h1>
          <p class="text-sm text-muted-foreground">用流程图写下一件事怎么做：每一步交给哪个岗位、先后顺序、哪些步骤并行、审查不通过退回到哪里。讨论串的组长拆任务时会参考项目的流程；项目没单独选时用默认流程（项目设置 → 高级）。</p>
        </div>
        <el-dropdown trigger="click" @command="(i: number) => startNew(options[i]!.draft)">
          <el-button type="primary" class="h-[38px]">新建流程</el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="(p, i) in options" :key="p.label" :command="i">{{ i === options.length - 1 ? "空白流程" : `从预设：${p.label}` }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </header>

      <Loading v-if="list.isLoading.value" />
      <EmptyState v-else-if="flows.length === 0 && !current" title="还没有工作流程" description="新建一个流程，或从预设开始。第一个流程会成为默认流程，所有项目的组长都会参考它。">
        <template #icon><FlowIcon /></template>
        <template #action>
          <div class="flex flex-wrap justify-center gap-2">
            <el-button v-for="p in options" :key="p.label" @click="startNew(p.draft)">{{ p.label }}</el-button>
          </div>
        </template>
      </EmptyState>

      <div v-else class="flex flex-col gap-4">
        <div class="flex flex-wrap gap-2" role="tablist" aria-label="我的流程">
          <button
            v-for="f in flows"
            :key="f.id"
            type="button"
            role="tab"
            :aria-selected="current?.id === f.id"
            :class="cn('flex h-9 items-center gap-2 rounded-lg border px-3 text-[13px]', current?.id === f.id ? 'border-brand bg-brand/10 font-medium' : 'bg-panel hover:bg-accent/60')"
            @click="open(f)"
          >
            {{ f.name }}
            <span v-if="f.is_default" class="rounded bg-muted px-1.5 text-[11px] text-muted-foreground">默认</span>
          </button>
          <span v-if="current && !current.id" class="flex h-9 items-center rounded-lg border border-dashed border-brand px-3 text-[13px] font-medium">{{ current.draft.name || "新流程" }}（未保存）</span>
        </div>

        <template v-if="current">
          <div class="grid gap-3 sm:grid-cols-[minmax(0,280px)_minmax(0,1fr)_auto] sm:items-end">
            <Field v-slot="{ id }" label="流程名称">
              <el-input :id="id" :model-value="current.draft.name" maxlength="40" @update:model-value="(v: string) => edit({ ...current!.draft, name: v })" />
            </Field>
            <Field v-slot="{ id }" label="说明（可选）">
              <el-input
                :id="id"
                :model-value="current.draft.description ?? ''"
                maxlength="2000"
                placeholder="什么样的事走这个流程"
                @update:model-value="(v: string) => edit({ ...current!.draft, description: v })"
              />
            </Field>
            <label class="flex h-9 items-center gap-2 text-[13px]">
              <el-switch :model-value="!!current.draft.is_default" @update:model-value="(v) => edit({ ...current!.draft, is_default: !!v })" />
              默认流程
            </label>
          </div>

          <FlowEditor :key="current.id ?? 'new'" :model-value="current.draft" :roles="choices" @update:model-value="edit" />

          <p v-if="loose.length" class="text-[13px] text-amber-700 dark:text-amber-300">这些步骤还没连线：{{ loose.join("、") }}。组长会把它们当作和别的步骤无关、可以单独做的事。</p>

          <p v-if="unlabeled" class="text-[13px] text-destructive">有 {{ unlabeled }} 条条件分支还没写条件，写上才能保存。</p>

          <div class="flex flex-wrap items-center justify-end gap-2">
            <ConfirmAction
              v-if="current.id"
              :title="`删除流程 ${current.draft.name}？`"
              description="选了这个流程的项目会改用默认流程；已经在做的讨论串下次开会话时生效。"
              @confirm="remove.mutate(current!.id!)"
            />
            <el-button v-if="dirty && current.id" @click="open(flows.find((f) => f.id === current!.id)!)">放弃修改</el-button>
            <el-button v-if="!current.id" @click="current = null; flows[0] && open(flows[0])">取消</el-button>
            <el-button type="primary" :loading="save.isPending.value" :disabled="!dirty || unlabeled > 0" @click="save.mutate(current!)">保存</el-button>
          </div>
        </template>
      </div>
    </div>
  </main>
</template>
