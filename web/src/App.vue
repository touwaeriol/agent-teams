<script setup lang="ts">
// Root: while `me` loads a spinner; a user who must change the password sees only that page;
// a sign-in / sign-out flips the route (the router guard decides where to go).
import en from "element-plus/es/locale/lang/en";
import ja from "element-plus/es/locale/lang/ja";
import ko from "element-plus/es/locale/lang/ko";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import { watch } from "vue";
import { RouterView, useRoute, useRouter } from "vue-router";
import { useMe } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import ChangePassword from "@/pages/ChangePassword.vue";
import { currentLocale, syncAccountLocale, type Locale } from "./i18n";
import { guard } from "./router";

// Element Plus speaks the same language (date pickers, pagination, empty tables).
const elementLocales: Record<Locale, typeof zhCn> = { "zh-CN": zhCn, en, ko, ja };
const elementLocale = elementLocales[currentLocale()];

const me = useMe();
const route = useRoute();
const router = useRouter();
watch(
  () => me.data.value ?? null,
  (user) => {
    if (me.isLoading.value) return;
    if (user && syncAccountLocale(user.locale ?? "")) return;
    const target = guard(route, user);
    if (target !== true) void router.replace(target);
  },
);
</script>

<template>
  <el-config-provider :locale="elementLocale">
    <Loading v-if="me.isLoading.value" class="h-full" />
    <ChangePassword v-else-if="me.data.value?.must_change_password" />
    <RouterView v-else />
  </el-config-provider>
</template>
