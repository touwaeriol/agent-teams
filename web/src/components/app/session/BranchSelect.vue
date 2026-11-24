<script setup lang="ts">
// The branch a session's repository starts from: the repository's branches (GitHub or a standalone
// Git repository), or any name typed in; empty = the default branch.
import { computed } from "vue";
import { useBranches, useGitRepoBranches } from "@/api/queries";
import type { RepoRef } from "../repo/repo";

const props = defineProps<{ item: RepoRef }>();
const value = defineModel<string>({ required: true });
const github = useBranches(
  () => (props.item.git_repo_id ? "" : (props.item.git_connection_id ?? "")),
  () => props.item.repo,
);
const standalone = useGitRepoBranches(() => props.item.git_repo_id ?? "");
const q = computed(() => (props.item.git_repo_id ? standalone : github));
const options = computed(() => (q.value.data.value ?? []) as string[]);
</script>

<template>
  <el-select
    v-model="value"
    filterable
    allow-create
    clearable
    default-first-option
    :loading="q.isLoading.value"
    placeholder="默认分支"
    :aria-label="`${item.repo} 的分支`"
    class="w-44 shrink-0"
  >
    <el-option v-for="b in options" :key="b" :label="b" :value="b" />
  </el-select>
</template>
