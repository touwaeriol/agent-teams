<script setup lang="ts">
// 管理后台 · 审计日志
import { $t } from "@/i18n";
import AdminPage from "./AdminPage.vue";
import AuditLog from "./AuditLog.vue";
</script>

<template>
  <AdminPage :title="$t('审计日志')">
    <AuditLog />
  </AdminPage>
</template>
