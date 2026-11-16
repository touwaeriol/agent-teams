<script setup lang="ts">
// 部署设置 · GitHub App (admin): register the platform GitHub App via GitHub's manifest flow.
import { useQueryClient } from "@tanstack/vue-query";
import { keys, useGitHubApp } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { useFlash } from "@/lib/flash";
import AppPanel from "./github/AppPanel.vue";

const app = useGitHubApp();
const qc = useQueryClient();
useFlash({ github_app: (slug) => `GitHub App ${slug} 已创建，用户现在可以一键连接 GitHub` }, () => void qc.invalidateQueries({ queryKey: keys.githubApp }));
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[760px] flex-col gap-5">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">部署设置 · GitHub App</h1>
          <p class="text-[13px] text-muted-foreground">部署后做一次。之后用户只在“GitHub 连接”页点“连接 GitHub”。</p>
        </div>
      </div>
      <AppPanel v-if="app.data.value" :status="app.data.value" />
      <Loading v-else />
    </div>
  </main>
</template>
