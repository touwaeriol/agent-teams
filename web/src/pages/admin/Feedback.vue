<script setup lang="ts">
// 管理后台 · 反馈
import { $t } from "@/i18n";
import AdminPage from "./AdminPage.vue";
import FeedbackList from "./FeedbackList.vue";
</script>

<template>
  <AdminPage :title="$t('反馈')">
    <FeedbackList />
  </AdminPage>
</template>
