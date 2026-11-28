<script setup lang="ts">
// 管理后台 · 内置命令和插件
import { $t } from "@/i18n";
import BuiltinLibrary from "@/components/app/library/BuiltinLibrary.vue";
import AdminPage from "./AdminPage.vue";
import BuiltinCommands from "./BuiltinCommands.vue";
</script>

<template>
  <AdminPage :title="$t('内置命令和插件')" :description="$t('所有用户都能用的内置命令、插件和技能。')">
    <BuiltinCommands />
    <BuiltinLibrary />
  </AdminPage>
</template>
