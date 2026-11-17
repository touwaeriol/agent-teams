<script setup lang="ts">
// 管理后台 · 用户
import AdminPage from "./AdminPage.vue";
import UsersTable from "./UsersTable.vue";
</script>

<template>
  <AdminPage title="用户" description="所有账号：角色、节点数、停用和重置密码。">
    <UsersTable />
  </AdminPage>
</template>
