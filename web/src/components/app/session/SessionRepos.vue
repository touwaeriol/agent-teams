<script setup lang="ts">
// A session's repositories, each with the branch its work starts from, and the picker to add more.
// `locked` maps repositories already checked out to their branch: shown, not changed.
import { $t } from "@/i18n";
import { X } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useConnections, useGitRepos } from "@/api/queries";
import AddRepo from "../repo/AddRepo.vue";
import { sourceLabel, type RepoRef } from "../repo/repo";
import BranchSelect from "./BranchSelect.vue";
import { repoDir, withBranches, type SessionRepo } from "./session";

const props = defineProps<{ locked?: Record<string, string>; currentProject?: string }>();
const value = defineModel<SessionRepo[]>({ required: true });
const connections = useConnections();
const conns = computed(() => connections.data.value ?? []);
const gitRepos = useGitRepos();
const canAdd = computed(() => conns.value.length > 0 || (gitRepos.data.value?.length ?? 0) > 0);
const refs = computed<RepoRef[]>({
  get: () => value.value,
  set: (list) => (value.value = withBranches(list, value.value)),
});
const lockedBranch = (repo: string) => props.locked?.[repo.toLowerCase()];
const setBranch = (i: number, branch: string) => (value.value = value.value.map((r, j) => (j === i ? { ...r, branch: branch ?? "" } : r)));
const remove = (i: number) => (value.value = value.value.filter((_, j) => j !== i));
</script>

<template>
  <div v-if="!connections.isLoading.value" class="flex flex-col gap-3">
    <p v-if="!canAdd && value.length === 0" class="text-[13px] leading-6 text-muted-foreground">
      {{ $t("还没有可用的仓库。") }}<RouterLink to="/settings/git" class="text-foreground underline underline-offset-2">{{ $t("先去连接 GitHub 或添加 Git 仓库") }}</RouterLink>。
    </p>
    <ol v-if="value.length > 0" class="flex flex-col divide-y rounded-lg border" data-testid="session-repos">
      <li v-for="(r, i) in value" :key="r.repo" class="flex items-center gap-3 px-3 py-2">
        <div class="flex min-w-0 flex-1 flex-col">
          <span class="truncate text-[13px] font-medium">{{ r.repo }}</span>
          <span class="truncate text-xs text-muted-foreground">./{{ repoDir(r.repo) }} · {{ sourceLabel(r, conns, gitRepos.data.value ?? []) }}</span>
        </div>
        <span v-if="lockedBranch(r.repo) !== undefined" class="w-44 shrink-0 truncate text-right text-xs text-muted-foreground">{{ $t("从") }} {{ lockedBranch(r.repo) || $t("默认分支") }}</span>
        <BranchSelect v-else :model-value="r.branch" :item="r" @update:model-value="(b: string) => setBranch(i, b)" />
        <button type="button" :aria-label="$t('移除 {repo}', { repo: r.repo })" :disabled="value.length === 1" class="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40" @click="remove(i)">
          <X class="size-4" />
        </button>
      </li>
    </ol>
    <AddRepo v-if="canAdd" v-model="refs" :connections="conns" :current-project="currentProject" />
  </div>
</template>
