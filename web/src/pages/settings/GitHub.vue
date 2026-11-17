<script setup lang="ts">
// GitHub 连接: “连接 GitHub” installs the platform App; each connection lists its repositories.
import { useQueryClient } from "@tanstack/vue-query";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { keys, useConnections, useGitHubApp, useMe } from "@/api/queries";
import Callout from "@/components/common/Callout.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { useFlash } from "@/lib/flash";
import ConnectionCard from "./github/ConnectionCard.vue";
import GitHubMark from "./github/GitHubMark.vue";

const qc = useQueryClient();
const me = useMe();
const app = useGitHubApp();
const connections = useConnections();
useFlash({ connected: (name) => `已连接：${name}` }, () => void qc.invalidateQueries({ queryKey: keys.connections }));
const installUrl = computed(() => (app.data.value?.configured ? app.data.value.install_url : undefined));
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[860px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">GitHub 连接</h1>
            <p class="max-w-[600px] text-[13px] leading-[22px] text-muted-foreground">
              点“连接 GitHub”跳到 GitHub，选你的账号或组织、勾选仓库，授权后自动回来。成员每次开工现取 1 小时有效的令牌，只能访问你勾选的仓库。
            </p>
          </div>
        </div>
        <span class="flex gap-2">
          <el-button v-if="installUrl" tag="a" :href="installUrl" type="primary" class="h-10"><GitHubMark /> 连接 GitHub</el-button>
        </span>
      </header>
      <Callout v-if="app.data.value && !app.data.value.configured" tone="warn">
        平台的 GitHub App 还没创建，“连接 GitHub”暂时不可用。<RouterLink v-if="me.data.value?.role === 'admin'" to="/admin/github-app">去创建</RouterLink><template v-else>请联系管理员。</template>
      </Callout>
      <Loading v-if="connections.isLoading.value" />
      <EmptyState v-if="connections.data.value?.length === 0" title="还没有连接" description="连接后，新建项目时就能直接选仓库。" />
      <ConnectionCard v-for="c in connections.data.value ?? []" :key="c.id" :connection="c" />
    </div>
  </main>
</template>
