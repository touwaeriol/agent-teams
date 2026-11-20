<script setup lang="ts">
/** 每个讨论串中都包含的仓库: a draft of the list (order, ✕ remove, + 添加 opens the searchable
 * picker of GitHub and standalone repositories); 保存 appears once it differs from the project. */
import { Plus } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { RouterLink } from "vue-router";
import { useConnections, useGitRepos } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import AddRepo from "@/components/app/repo/AddRepo.vue";
import RepoRow from "@/components/app/repo/RepoRow.vue";
import { moveRepo, removeRepo, REPOS_HINT, reposPayload, type RepoRef } from "@/components/app/repo/repo";
import { repoRefs } from "./logic";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const saved = computed(() => JSON.stringify(repoRefs(props.project)));
const draft = ref<RepoRef[]>(JSON.parse(saved.value) as RepoRef[]);
watch(saved, (s) => (draft.value = JSON.parse(s) as RepoRef[]));
const update = useUpdate(() => props.project.id, "仓库设置已保存");
const dirty = computed(() => JSON.stringify(draft.value) !== saved.value);
const connections = useConnections();
const list = computed(() => connections.data.value ?? []);
const gitRepos = useGitRepos();
/** Something to pick from: a GitHub connection or a standalone repository. */
const canAdd = computed(() => list.value.length > 0 || (gitRepos.data.value?.length ?? 0) > 0);
const adding = ref(false);
</script>

<template>
  <section class="flex flex-col rounded-xl border" aria-label="每个讨论串中都包含的仓库">
    <div class="flex min-h-12 items-center justify-between gap-3 px-4">
      <span class="text-[13px] text-muted-foreground">每个讨论串中都包含的仓库</span>
      <span class="flex items-center gap-1">
        <el-button v-if="dirty" size="small" type="primary" :disabled="update.isPending.value" @click="update.mutate({ repos: reposPayload(draft) })">保存</el-button>
        <el-button v-if="canAdd" size="small" text :aria-expanded="adding" @click="adding = !adding"><Plus />添加</el-button>
      </span>
    </div>
    <ol v-if="draft.length > 0" class="flex flex-col divide-y border-t">
      <RepoRow
        v-for="(r, i) in draft"
        :key="r.repo"
        :item="r"
        :index="i"
        :count="draft.length"
        :connections="list"
        @move="(d: number) => (draft = moveRepo(draft, i, d))"
        @remove="draft = removeRepo(draft, i)"
      />
    </ol>
    <p v-else-if="!connections.isLoading.value" class="border-t px-4 py-3 text-[13px] text-muted-foreground">
      <template v-if="canAdd">未关联仓库：员工只能在空目录里工作。</template>
      <template v-else>还没有可用的仓库。<RouterLink to="/settings/git" class="text-foreground underline underline-offset-2">先去连接 GitHub 或添加 Git 仓库</RouterLink>，就能在这里选仓库。</template>
    </p>
    <div v-if="adding && canAdd" class="border-t px-4 py-3">
      <AddRepo v-model="draft" :connections="list" :current-project="project.id" />
    </div>
    <p class="border-t px-4 py-2.5 text-[12px] leading-5 text-muted-foreground">{{ REPOS_HINT }}</p>
  </section>
</template>
