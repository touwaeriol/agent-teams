<script setup lang="ts">
// 会话信息 of a session: 环境 (the node and whether it is online, the role and agent (CLI) — the
// three are fixed once the session starts — every repository with the
// branch it started from, the work branch, the working directory on the node) and 上下文 (the
// total and its breakdown, Claude only; hidden for runtimes without a breakdown). Repositories can be added or removed while it runs.
import { Folder, FolderGit2 } from "lucide-vue-next";
import { computed, ref } from "vue";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ProjectView, ThreadView } from "@/api/views";
import { INK, leadOf } from "@/lib/present";
import { runtimeLabel } from "@/lib/runtimes";
import SectionLabel from "../SectionLabel.vue";
import SessionContextPanel from "./SessionContextPanel.vue";
import SessionRepos from "./SessionRepos.vue";
import { repoDir, sessionReposPayload, type SessionRepo } from "./session";

const props = defineProps<{ project: ProjectView; thread: ThreadView }>();
const lead = computed(() => leadOf(props.thread));
const node = computed(() => lead.value?.node ?? null);
const cli = computed(() => runtimeLabel(props.thread.session?.runtime || lead.value?.runtime));
const cwd = computed(() => props.thread.session?.cwd ?? "");
/** Base branch per repository (lowercase), as the thread recorded it ("" = default branch). */
const bases = computed(() => {
  const out: Record<string, string> = {};
  for (const r of props.project.repos ?? []) out[r.repo.toLowerCase()] = "";
  for (const r of props.thread.repos ?? []) out[r.repo.toLowerCase()] = r.base_branch ?? "";
  return out;
});
const current = computed<SessionRepo[]>(() => (props.project.repos ?? []).map((r) => ({ ...r, git_connection_id: r.git_connection_id ?? null, branch: bases.value[r.repo.toLowerCase()] ?? "" })));
const dirs = computed(() => current.value.map((r) => `./${repoDir(r.repo)}`).join("　"));
const editing = ref(false);
const draft = ref<SessionRepo[]>([]);
function edit() {
  draft.value = current.value.map((r) => ({ ...r }));
  editing.value = true;
}
const save = useAction(() => sessions.setRepos(props.project.id, sessionReposPayload(draft.value)), {
  invalidate: () => [keys.projects, keys.project(props.project.id), keys.threads(props.project.id)],
  success: "仓库已更新，员工会收到通知",
  onSuccess: () => (editing.value = false),
});
</script>

<template>
  <div class="flex flex-col gap-6 p-4 text-[13px]" data-testid="session-workspace">
    <section class="flex flex-col gap-2.5">
      <SectionLabel>环境</SectionLabel>
      <dl class="grid grid-cols-[44px_minmax(0,1fr)] items-start gap-x-3 gap-y-2.5">
        <dt class="text-xs leading-5 text-muted-foreground">节点</dt>
        <dd class="flex min-w-0 items-center gap-1.5 leading-5" data-testid="env-node">
          <template v-if="node">
            <span class="size-2 shrink-0 rounded-full" :style="{ background: node.online ? INK.done : INK.idle }" />
            <span class="truncate font-medium">{{ node.name }}</span>
            <span class="shrink-0 text-xs text-muted-foreground">{{ node.online ? "在线" : "离线" }}</span>
          </template>
          <span v-else class="text-muted-foreground">等待分配节点</span>
        </dd>
        <dt class="text-xs leading-5 text-muted-foreground">岗位</dt>
        <dd class="truncate leading-5" data-testid="env-role">{{ lead?.title || lead?.role || "—" }}</dd>
        <dt class="text-xs leading-5 text-muted-foreground">Agent</dt>
        <dd class="leading-5" data-testid="env-agent">{{ cli }}</dd>
        <dt class="text-xs leading-5 text-muted-foreground">仓库</dt>
        <dd class="flex min-w-0 flex-col gap-1.5">
          <template v-if="!editing">
            <span v-if="current.length === 0" class="flex min-w-0 items-center gap-1.5 leading-5" data-testid="env-local">
              <Folder class="size-3.5 shrink-0 text-muted-foreground" />
              <span class="truncate">本地文件夹（固定在节点 {{ node?.name || "开工时的节点" }}）</span>
            </span>
            <span v-for="r in current" :key="r.repo" class="flex min-w-0 items-center gap-1.5 leading-5">
              <FolderGit2 class="size-3.5 shrink-0 text-muted-foreground" />
              <span class="truncate font-mono text-xs" :title="r.repo">{{ r.repo }}</span>
              <span class="shrink-0 text-xs text-muted-foreground">从 {{ r.branch || "默认分支" }}</span>
            </span>
            <button type="button" class="self-start text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" @click="edit">{{ current.length ? "增删仓库" : "添加仓库" }}</button>
          </template>
          <template v-else>
            <SessionRepos v-model="draft" :locked="bases" :current-project="project.id" />
            <div class="flex justify-end gap-2">
              <el-button size="small" @click="editing = false">取消</el-button>
              <el-button size="small" type="primary" :loading="save.isPending.value" @click="save.mutate()">保存</el-button>
            </div>
          </template>
        </dd>
        <dt v-if="current.length" class="text-xs leading-5 text-muted-foreground">分支</dt>
        <dd v-if="current.length" class="flex min-w-0 flex-col gap-1">
          <span class="truncate rounded-md bg-raised px-1.5 py-0.5 font-mono text-xs leading-5" :title="thread.branch">{{ thread.branch }}</span>
          <a v-if="thread.pr_url" :href="thread.pr_url" target="_blank" rel="noopener" class="text-xs underline underline-offset-2">PR #{{ thread.pr_number }}</a>
        </dd>
        <dt class="text-xs leading-5 text-muted-foreground">目录</dt>
        <dd class="flex min-w-0 flex-col gap-1" data-testid="env-cwd">
          <span v-if="cwd" class="at-wrap rounded-md bg-raised px-1.5 py-0.5 font-mono text-xs leading-5" :title="cwd">{{ cwd }}</span>
          <span v-else class="text-xs leading-5 text-muted-foreground">员工开工后显示</span>
          <span v-if="dirs" class="truncate font-mono text-[11.5px] text-muted-foreground">{{ dirs }}</span>
          <span v-else-if="cwd" class="text-[11.5px] leading-4 text-muted-foreground">没有 git 仓库，文件只在这台节点上；删除会话时才清理</span>
        </dd>
      </dl>
    </section>
    <SessionContextPanel :thread="thread" />
  </div>
</template>
