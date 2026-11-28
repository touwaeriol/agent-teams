<script setup lang="ts">
// 管理后台 · 用户
import { $t } from "@/i18n";
import { Plus } from "lucide-vue-next";
import { ref } from "vue";
import AdminPage from "./AdminPage.vue";
import CreateUserDialog from "./CreateUserDialog.vue";
import UsersTable from "./UsersTable.vue";

const creating = ref(false);
</script>

<template>
  <AdminPage :title="$t('用户')" :description="$t('所有账号：角色、节点数、停用和重置密码。')">
    <template #actions>
      <el-button type="primary" @click="creating = true"><Plus class="mr-1 size-4" />{{ $t("新建用户") }}</el-button>
    </template>
    <UsersTable />
    <CreateUserDialog v-model:open="creating" />
  </AdminPage>
</template>
