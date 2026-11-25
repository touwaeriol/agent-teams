<script setup lang="ts">
// The repo picker: the source (a GitHub connection, or the standalone Git repositories added under
// 设置 → Git 仓库) and a searchable list of its repositories ("已被 X 使用" / private lock); for GitHub
// a pasted URL or typed owner/repo works too.
import { Lock } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { errorText } from "@/api/mutations";
import { useGitRepos, useRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import Combobox from "@/components/common/Combobox.vue";
import Field from "@/components/common/Field.vue";
import Callout from "@/components/common/Callout.vue";
import { addRepo, connectionLabel, isPatConnection, matchesRepo, PAT_WARNING, repoOptions, STANDALONE, standaloneOptions, type RepoRef } from "./repo";

const props = defineProps<{ connections: GitConnectionView[]; currentProject?: string }>();
const value = defineModel<RepoRef[]>({ required: true });
const gitRepos = useGitRepos();
const standalone = computed(() => gitRepos.data.value ?? []);
const source = ref(props.connections[0]?.id ?? STANDALONE);
const isStandalone = computed(() => source.value === STANDALONE);
const connectionId = computed(() => (isStandalone.value ? "" : source.value));
const repos = useRepos(connectionId);
const picked = computed(() => props.connections.find((c) => c.id === connectionId.value));
const owner = computed(() => picked.value?.login ?? "");
const options = computed(() =>
  isStandalone.value ? standaloneOptions(standalone.value, value.value, props.currentProject) : repoOptions(repos.data.value?.items ?? [], value.value, props.currentProject),
);
const hint = computed(() => {
  if (isStandalone.value) return "在 设置 → Git 仓库 里单独添加的仓库（GitLab、Gitea、自建 Git）。没有 PR 和 CI：讨论串的改动留在讨论串分支上。";
  return repos.error.value ? errorText(repos.error.value) : `来自你连接的 GitHub（${owner.value}），输入即可搜索，也能粘贴链接；选中就加入列表。`;
});
const sources = computed(() => props.connections.length + (standalone.value.length ? 1 : 0));
function add(repo: string) {
  if (!isStandalone.value) {
    value.value = addRepo(value.value, { repo, git_connection_id: connectionId.value });
    return;
  }
  const g = standalone.value.find((r) => r.slug === repo);
  if (g) value.value = addRepo(value.value, { repo: g.slug, git_connection_id: null, git_repo_id: g.id });
}
</script>

<template>
  <div class="grid gap-3 sm:grid-cols-[2fr_3fr]">
    <Field v-if="sources > 1" v-slot="{ id }" label="来源">
      <el-select :id="id" v-model="source" placeholder="选择来源">
        <el-option v-for="c in connections" :key="c.id" :value="c.id" :label="connectionLabel(c)" />
        <el-option v-if="standalone.length" :value="STANDALONE" label="单独添加的 Git 仓库" />
      </el-select>
    </Field>
    <Field v-slot="{ id }" :label="value.length ? '再加一个仓库' : '添加仓库'" :hint="hint" :class="sources > 1 ? undefined : 'sm:col-span-2'">
      <!-- a fresh, empty picker after each added repository -->
      <Combobox
        :id="id"
        :key="`${value.length}-${source}`"
        :options="options"
        :filter="matchesRepo"
        :loading="isStandalone ? gitRepos.isLoading.value : repos.isLoading.value"
        placeholder="搜索仓库"
        empty="没有匹配的仓库"
        :allow-custom="!isStandalone"
        class="at-combobox--mono"
        @pick="add"
      >
        <template #option="{ option }"><span class="font-mono text-[13px]">{{ option.value }}</span></template>
        <template #hint="{ option }">
          <template v-if="option.usedBy">{{ option.usedBy }}</template>
          <Lock v-else-if="option.private" class="size-3.5" aria-label="私有" />
        </template>
      </Combobox>
      <RouterLink v-if="isStandalone" to="/settings/git" class="text-[12px] text-muted-foreground underline underline-offset-2">添加新的 Git 仓库…</RouterLink>
    </Field>
    <Callout v-if="isPatConnection(picked)" tone="warn" class="sm:col-span-2" data-pat-warning>{{ PAT_WARNING }}</Callout>
  </div>
</template>
