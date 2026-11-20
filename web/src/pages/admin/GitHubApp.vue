<script setup lang="ts">
// 管理后台 · GitHub App: register the platform GitHub App via GitHub's manifest flow.
// GitHub's callback lands here (or on the old /setup/github) with ?github_app=<slug> or ?error=….
import { useQueryClient } from "@tanstack/vue-query";
import { keys, useGitHubApp } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { useFlash } from "@/lib/flash";
import AdminPage from "./AdminPage.vue";
import AppPanel from "./AppPanel.vue";

const app = useGitHubApp();
const qc = useQueryClient();
useFlash({ github_app: (slug) => `GitHub App ${slug} 已创建，用户现在可以一键连接 GitHub` }, () => void qc.invalidateQueries({ queryKey: keys.githubApp }));
</script>

<template>
  <AdminPage title="GitHub App" description="用户“连接 GitHub”时安装的平台 App，部署后创建一次。之后用户只在“Git 仓库”页点“连接 GitHub”。">
    <AppPanel v-if="app.data.value" :status="app.data.value" />
    <Loading v-else />
  </AdminPage>
</template>
