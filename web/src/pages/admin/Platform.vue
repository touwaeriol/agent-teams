<script setup lang="ts">
// 管理后台 · 平台设置
import { $t } from "@/i18n";
import AdminPage from "./AdminPage.vue";
import PlatformSettings from "./PlatformSettings.vue";
import VoiceSettings from "./VoiceSettings.vue";
</script>

<template>
  <AdminPage :title="$t('平台设置')" :description="$t('注册开关、每人限额和语音输入，对所有用户生效。')">
    <PlatformSettings />
    <VoiceSettings />
  </AdminPage>
</template>
