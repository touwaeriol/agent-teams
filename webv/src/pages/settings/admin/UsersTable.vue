<script setup lang="ts">
import { computed, ref } from "vue";
import { useAdminUsers, useMe } from "@/api/queries";
import CopyBlock from "@/components/common/CopyBlock.vue";
import InfoDialog from "@/components/common/InfoDialog.vue";
import Loading from "@/components/common/Loading.vue";
import { USER_COLUMNS } from "./admin";
import UserRow from "./UserRow.vue";

const users = useAdminUsers();
const me = useMe();
const temp = ref<{ email: string; password: string } | null>(null);
const shown = computed({ get: () => Boolean(temp.value), set: (v: boolean) => !v && (temp.value = null) });
</script>

<template>
  <div class="overflow-x-auto rounded-[14px] border bg-panel">
    <table class="w-full min-w-[720px] border-collapse text-[13px]">
      <thead>
        <tr class="bg-background text-left text-muted-foreground">
          <th v-for="h in USER_COLUMNS" :key="h" class="px-4 py-3 font-medium">{{ h }}</th>
        </tr>
      </thead>
      <tbody>
        <UserRow v-for="u in users.data.value ?? []" :key="u.id" :user="u" :self="u.id === me.data.value?.id" @temp="(password: string) => (temp = { email: u.email, password })" />
      </tbody>
    </table>
    <Loading v-if="users.isLoading.value" />
    <InfoDialog v-model:open="shown" title="临时密码" :description="`把它交给 ${temp?.email}，登录后必须修改。只显示这一次。`">
      <CopyBlock v-if="temp" :text="temp.password" />
    </InfoDialog>
  </div>
</template>
