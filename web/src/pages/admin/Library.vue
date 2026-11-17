<script setup lang="ts">
// 管理后台 · 内置命令和插件
import BuiltinLibrary from "@/components/app/library/BuiltinLibrary.vue";
import AdminPage from "./AdminPage.vue";
import BuiltinCommands from "./BuiltinCommands.vue";
</script>

<template>
  <AdminPage title="内置命令和插件" description="所有用户都能用的内置命令、插件和技能。">
    <BuiltinCommands />
    <BuiltinLibrary />
  </AdminPage>
</template>
