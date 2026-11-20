<script setup lang="ts">
/** Prototype of 设置 → Git 仓库 (docs/v2/git-repos.md §11.2), dev only at /settings/git-prototype.
 * ?dialog=https|ssh opens 添加仓库 with a sample; &tested=1 shows the 测试连接 result. */
import { ref } from "vue";
import { useRoute } from "vue-router";
import { useConnections } from "@/api/queries";
import NavButton from "@/components/shell/NavButton.vue";
import ConnectionCard from "@/pages/settings/github/ConnectionCard.vue";
import GitHubMark from "@/pages/settings/github/GitHubMark.vue";
import AddRepoDialog from "./AddRepoDialog.vue";
import CredentialTable from "./CredentialTable.vue";
import { repos } from "./fixtures";
import RepoCard from "./RepoCard.vue";

const route = useRoute();
const preset = route.query.dialog === "ssh" ? "ssh" : "https";
const dialog = ref(route.query.dialog === "https" || route.query.dialog === "ssh");
const tested = route.query.tested === "1";
const connections = useConnections();
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[960px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">Git 仓库</h1>
            <p class="max-w-[640px] text-[13px] leading-[22px] text-muted-foreground">
              项目和讨论串能用的仓库。GitHub 用 App 连接，PR 和 CI 都能用；其他仓库（GitLab、Gitea、自建 Git）在这里单独添加，用 HTTPS 令牌或 SSH 私钥验证。凭据只保存在平台上，节点上的 git 经平台中转读写，拿不到凭据。
            </p>
          </div>
        </div>
        <span class="flex gap-2 [&>.el-button]:!ml-0">
          <el-button class="h-[38px]"><GitHubMark /> 连接 GitHub</el-button>
          <el-button type="primary" class="h-[38px]" @click="dialog = true">添加仓库</el-button>
        </span>
      </header>

      <section class="flex flex-col gap-3" aria-label="单独添加的仓库">
        <h2 class="text-[15px] font-semibold">单独添加的仓库 <span class="font-normal text-muted-foreground">· {{ repos.length }}</span></h2>
        <RepoCard v-for="r in repos" :key="r.id" :repo="r" @edit="dialog = true" />
      </section>

      <section class="flex flex-col gap-3" aria-label="凭据">
        <div class="flex items-baseline justify-between">
          <h2 class="text-[15px] font-semibold">凭据</h2>
          <span class="text-xs text-muted-foreground">同一主机的仓库可以共用一条凭据；替换后所有用它的仓库立即生效</span>
        </div>
        <CredentialTable />
      </section>

      <section class="flex flex-col gap-3" aria-label="GitHub 连接">
        <div class="flex items-baseline justify-between">
          <h2 class="text-[15px] font-semibold">GitHub 连接</h2>
          <span class="text-xs text-muted-foreground">经 GitHub App 授权的仓库，PR、CI 检查和自动合并都可用</span>
        </div>
        <ConnectionCard v-for="c in connections.data.value ?? []" :key="c.id" :connection="c" />
      </section>
    </div>
    <AddRepoDialog v-model:open="dialog" :preset="preset" :tested="tested" />
  </main>
</template>
