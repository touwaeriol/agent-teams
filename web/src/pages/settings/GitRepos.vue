<script setup lang="ts">
// Git 仓库 (docs/v2/git-repos.md §11.2): standalone repositories (GitLab, Gitea, self-hosted Git; HTTPS
// token or SSH key, reached through the console's relay), the credentials they share, and the GitHub
// connections ("连接 GitHub" installs the platform App; PRs and CI work there).
import { $t } from "@/i18n";
import { useQueryClient } from "@tanstack/vue-query";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { keys, useConnections, useGitCredentials, useGitHubApp, useGitRepos, useMe } from "@/api/queries";
import type { GitCredentialView, GitRepoView } from "@/api/views";
import Callout from "@/components/common/Callout.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { useFlash } from "@/lib/flash";
import ConnectionCard from "./github/ConnectionCard.vue";
import GitHubMark from "./github/GitHubMark.vue";
import AddRepoDialog from "./gitRepos/AddRepoDialog.vue";
import CredentialDialog from "./gitRepos/CredentialDialog.vue";
import CredentialTable from "./gitRepos/CredentialTable.vue";
import RepoCard from "./gitRepos/RepoCard.vue";

const qc = useQueryClient();
const me = useMe();
const app = useGitHubApp();
const connections = useConnections();
const repos = useGitRepos();
const credentials = useGitCredentials();
useFlash({ connected: (name) => $t("已连接：{name}", { name }) }, () => void qc.invalidateQueries({ queryKey: keys.connections }));
const installUrl = computed(() => (app.data.value?.configured ? app.data.value.install_url : undefined));
const repoList = computed(() => repos.data.value ?? []);
const credList = computed(() => credentials.data.value ?? []);

const dialog = ref(false);
const editing = ref<GitRepoView | null>(null);
function openRepo(r: GitRepoView | null) {
  editing.value = r;
  dialog.value = true;
}
const credDialog = ref(false);
const replacing = ref<GitCredentialView | null>(null);
function openCredential(c: GitCredentialView) {
  replacing.value = c;
  credDialog.value = true;
}
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">{{ $t("Git 仓库") }}</h1>
            <p class="max-w-[640px] text-[13px] leading-[22px] text-muted-foreground">
              {{ $t("项目和讨论串能用的仓库。GitHub 用 App 连接，PR 和 CI 都能用；其他仓库（GitLab、Gitea、自建 Git）在这里单独添加，用 HTTPS 令牌或 SSH 私钥验证。凭据加密保存在平台上，员工的 git 用到时才交给节点，只放在内存里，只给这个项目员工会话里的 git 用。") }}
            </p>
          </div>
        </div>
        <span class="flex gap-2 [&>.el-button]:!ml-0">
          <el-button v-if="installUrl" tag="a" :href="installUrl" class="h-[38px]"><GitHubMark /> {{ $t("连接 GitHub") }}</el-button>
          <el-button type="primary" class="h-[38px]" @click="openRepo(null)">{{ $t("添加仓库") }}</el-button>
        </span>
      </header>

      <section class="flex flex-col gap-3" :aria-label="$t('单独添加的仓库')">
        <h2 class="text-[15px] font-semibold">{{ $t("单独添加的仓库") }} <span class="font-normal text-muted-foreground">· {{ repoList.length }}</span></h2>
        <Loading v-if="repos.isLoading.value" />
        <EmptyState v-else-if="repoList.length === 0" :title="$t('还没有单独添加的仓库')" :description="$t('点“添加仓库”，粘贴 GitLab、Gitea 或自建 Git 的克隆地址，填令牌或生成部署密钥，然后测试连接。')" />
        <RepoCard v-for="r in repoList" :key="r.id" :repo="r" :credentials="credList" @edit="openRepo(r)" />
      </section>

      <section v-if="credList.length" class="flex flex-col gap-3" :aria-label="$t('凭据')">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <h2 class="text-[15px] font-semibold">{{ $t("凭据") }}</h2>
          <span class="text-xs text-muted-foreground">{{ $t("同一主机的仓库可以共用一条凭据；替换后所有用它的仓库立即生效") }}</span>
        </div>
        <CredentialTable :credentials="credList" @replace="openCredential" />
      </section>

      <section class="flex flex-col gap-3" :aria-label="$t('GitHub 连接')">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <h2 class="text-[15px] font-semibold">{{ $t("GitHub 连接") }}</h2>
          <span class="text-xs text-muted-foreground">{{ $t("经 GitHub App 授权的仓库，PR、CI 检查和自动合并都可用") }}</span>
        </div>
        <Callout v-if="app.data.value && !app.data.value.configured" tone="warn">
          {{ $t("平台的 GitHub App 还没创建，“连接 GitHub”暂时不可用。") }}<RouterLink v-if="me.data.value?.role === 'admin'" to="/admin/github-app">{{ $t("去创建") }}</RouterLink><template v-else>{{ $t("请联系管理员。") }}</template>
        </Callout>
        <Loading v-if="connections.isLoading.value" />
        <EmptyState v-if="connections.data.value?.length === 0" :title="$t('还没有连接 GitHub')" :description="$t('点“连接 GitHub”跳到 GitHub，选账号或组织、勾选仓库，授权后自动回来。')" />
        <ConnectionCard v-for="c in connections.data.value ?? []" :key="c.id" :connection="c" />
      </section>
    </div>
    <AddRepoDialog v-model:open="dialog" :repo="editing" :credentials="credList" />
    <CredentialDialog v-model:open="credDialog" :credential="replacing" />
  </main>
</template>
