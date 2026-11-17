<script setup lang="ts">
// 管理: platform switches, the user table, user feedback, the model list, built-in commands, plugins and skills, the GitHub App, audit log.
import { RouterLink } from "vue-router";
import Card from "@/components/app/Card.vue";
import BuiltinLibrary from "@/components/app/library/BuiltinLibrary.vue";
import NavButton from "@/components/shell/NavButton.vue";
import AuditLog from "./admin/AuditLog.vue";
import BuiltinCommands from "./admin/BuiltinCommands.vue";
import FeedbackList from "./admin/FeedbackList.vue";
import PlatformModels from "./admin/PlatformModels.vue";
import PlatformSettings from "./admin/PlatformSettings.vue";
import UsersTable from "./admin/UsersTable.vue";
import VoiceSettings from "./admin/VoiceSettings.vue";
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[980px] flex-col gap-5">
      <div class="flex items-center gap-1">
        <NavButton />
        <h1 class="text-2xl font-semibold">管理</h1>
      </div>
      <PlatformSettings />
      <UsersTable />
      <FeedbackList />
      <PlatformModels />
      <BuiltinCommands />
      <BuiltinLibrary />
      <Card class="flex-row flex-wrap items-center justify-between gap-3">
        <span class="flex flex-col gap-0.5">
          <span class="font-semibold">GitHub App</span>
          <span class="text-xs text-muted-foreground">用户“连接 GitHub”时安装的平台 App，部署后创建一次。</span>
        </span>
        <RouterLink to="/setup/github" class="text-[13px] underline-offset-2 hover:underline">查看或创建</RouterLink>
      </Card>
      <VoiceSettings />
      <AuditLog />
    </div>
  </main>
</template>
