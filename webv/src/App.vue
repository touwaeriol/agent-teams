<script setup lang="ts">
// Root: while `me` loads a spinner; a user who must change the password sees only that page;
// a sign-in / sign-out flips the route (the router guard decides where to go).
import zhCn from "element-plus/es/locale/lang/zh-cn";
import { watch } from "vue";
import { RouterView, useRoute, useRouter } from "vue-router";
import { useMe } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import ChangePassword from "@/pages/ChangePassword.vue";
import { guard } from "./router";

const me = useMe();
const route = useRoute();
const router = useRouter();
watch(
  () => me.data.value ?? null,
  (user) => {
    if (me.isLoading.value) return;
    const target = guard(route, user);
    if (target !== true) void router.replace(target);
  },
);
</script>

<template>
  <el-config-provider :locale="zhCn">
    <Loading v-if="me.isLoading.value" class="h-full" />
    <ChangePassword v-else-if="me.data.value?.must_change_password" />
    <RouterView v-else />
  </el-config-provider>
</template>
