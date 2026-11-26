<script setup lang="ts">
// A project's repositories: the ordered list (move up / down, remove) and the searchable picker to
// add more. `v-model` is the list; `current-project` hides "used by" for the project itself.
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useConnections, useGitRepos } from "@/api/queries";
import AddRepo from "./AddRepo.vue";
import RepoRow from "./RepoRow.vue";
import { moveRepo, removeRepo, setBase, REPOS_HINT, type RepoRef } from "./repo";

defineProps<{ currentProject?: string }>();
const value = defineModel<RepoRef[]>({ required: true });
const connections = useConnections();
const list = computed(() => connections.data.value ?? []);
const gitRepos = useGitRepos();
const canAdd = computed(() => list.value.length > 0 || (gitRepos.data.value?.length ?? 0) > 0);
</script>

<template>
  <template v-if="!connections.isLoading.value">
    <p v-if="!canAdd && value.length === 0" class="text-[13px] leading-6 text-muted-foreground">
      还没有可用的仓库。<RouterLink to="/settings/git" class="text-foreground underline underline-offset-2">先去连接 GitHub 或添加 Git 仓库</RouterLink>，就能在这里选仓库；也可以先不关联仓库。
    </p>
    <div v-else class="flex flex-col gap-3">
      <ol v-if="value.length > 0" class="flex flex-col divide-y rounded-lg border">
        <RepoRow
          v-for="(r, i) in value"
          :key="r.repo"
          :item="r"
          :index="i"
          :count="value.length"
          :connections="list"
          @move="(d: number) => (value = moveRepo(value, i, d))"
          @remove="value = removeRepo(value, i)"
          @base="(b: string) => (value = setBase(value, i, b))"
        />
      </ol>
      <p v-else class="text-[13px] text-muted-foreground">未关联仓库：员工只能在空目录里工作。</p>
      <AddRepo v-if="canAdd" v-model="value" :connections="list" :current-project="currentProject" />
      <p class="text-[12px] leading-5 text-muted-foreground">{{ REPOS_HINT }}</p>
    </div>
  </template>
</template>
