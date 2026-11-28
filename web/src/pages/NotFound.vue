<script setup lang="ts">
// 404: any address that matches no page, and admin console addresses for non-admins
// (the router guard renders this without changing the URL, so they look the same).
import { $t } from "@/i18n";
import { RouterLink } from "vue-router";
import Logo from "@/components/common/Logo.vue";
</script>

<template>
  <main class="flex min-h-full items-center justify-center px-4 py-12">
    <div class="flex max-w-[380px] flex-col items-center gap-4 text-center">
      <Logo class="size-10" />
      <p class="text-5xl font-semibold tracking-tight">404</p>
      <h1 class="text-xl font-semibold">{{ $t("页面不存在") }}</h1>
      <p class="text-[13px] leading-5 text-muted-foreground">{{ $t("这个地址没有对应的页面，可能输错了，或者页面已经移走。") }}</p>
      <RouterLink to="/projects" class="mt-2 inline-flex h-9 items-center rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground hover:bg-primary/90">{{ $t("返回首页") }}</RouterLink>
    </div>
  </main>
</template>
