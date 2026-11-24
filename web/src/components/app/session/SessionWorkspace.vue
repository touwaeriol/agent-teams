<script setup lang="ts">
// 工作现场 of a session: the employee and its node, the work branch, and the folder with every
// repository side by side (each from its branch). Repositories can be added or removed while it runs.
import { FolderGit2, GitBranch, Server } from "lucide-vue-next";
import { computed, ref } from "vue";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ProjectView, ThreadView } from "@/api/views";
import { leadOf } from "@/lib/present";
import SectionLabel from "../SectionLabel.vue";
import SessionRepos from "./SessionRepos.vue";
import { repoDir, sessionReposPayload, type SessionRepo } from "./session";

const props = defineProps<{ project: ProjectView; thread: ThreadView }>();
const lead = computed(() => leadOf(props.thread));
/** Base branch per repository (lowercase), as the thread recorded it ("" = default branch). */
const bases = computed(() => {
  const out: Record<string, string> = {};
  for (const r of props.project.repos ?? []) out[r.repo.toLowerCase()] = "";
  for (const r of props.thread.repos ?? []) out[r.repo.toLowerCase()] = r.base_branch ?? "";
  return out;
});
const current = computed<SessionRepo[]>(() => (props.project.repos ?? []).map((r) => ({ ...r, git_connection_id: r.git_connection_id ?? null, branch: bases.value[r.repo.toLowerCase()] ?? "" })));
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
  <div class="flex flex-col gap-5 p-4 text-[13px]" data-testid="session-workspace">
    <section class="flex flex-col gap-2">
      <SectionLabel>员工</SectionLabel>
      <div v-if="lead" class="flex flex-col gap-1">
        <span class="font-medium">{{ lead.title || lead.name }}</span>
        <span class="flex items-center gap-1.5 text-muted-foreground">
          <Server class="size-3.5" />
          {{ lead.node ? `${lead.node.name}${lead.node.online ? "" : "（离线）"}` : "等待分配节点" }}
        </span>
      </div>
      <span v-else class="text-muted-foreground">还没有员工</span>
    </section>
    <section class="flex flex-col gap-2">
      <SectionLabel>工作分支</SectionLabel>
      <span class="flex items-center gap-1.5 font-mono text-xs"><GitBranch class="size-3.5 shrink-0" />{{ thread.branch }}</span>
      <a v-if="thread.pr_url" :href="thread.pr_url" target="_blank" rel="noopener" class="text-xs underline underline-offset-2">PR #{{ thread.pr_number }}</a>
    </section>
    <section class="flex flex-col gap-2">
      <div class="flex items-center justify-between">
        <SectionLabel>工作目录</SectionLabel>
        <button v-if="!editing" type="button" class="text-xs underline-offset-2 hover:underline" @click="edit">增删仓库</button>
      </div>
      <template v-if="!editing">
        <ul class="flex flex-col gap-1.5">
          <li v-for="r in current" :key="r.repo" class="flex items-start gap-2">
            <FolderGit2 class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
            <span class="flex min-w-0 flex-col">
              <span class="truncate font-mono text-xs">./{{ repoDir(r.repo) }}</span>
              <span class="truncate text-xs text-muted-foreground">{{ r.repo }} · 从 {{ r.branch || "默认分支" }}</span>
            </span>
          </li>
        </ul>
      </template>
      <template v-else>
        <SessionRepos v-model="draft" :locked="bases" :current-project="project.id" />
        <div class="flex justify-end gap-2">
          <el-button size="small" @click="editing = false">取消</el-button>
          <el-button size="small" type="primary" :loading="save.isPending.value" :disabled="draft.length === 0" @click="save.mutate()">保存</el-button>
        </div>
      </template>
    </section>
  </div>
</template>
