<script setup lang="ts">
import Logo from "@/components/common/Logo.vue";

defineProps<{ subtitle: string }>();
</script>

<template>
  <div class="flex min-h-full items-center justify-center px-4 py-12">
    <div class="flex w-full max-w-[380px] flex-col gap-7">
      <div class="flex flex-col items-center gap-3 text-center">
        <Logo class="size-10" />
        <h1 class="text-[26px] font-semibold tracking-tight">Agent Teams</h1>
        <p class="leading-[22px] text-muted-foreground">{{ subtitle }}</p>
      </div>
      <slot />
    </div>
  </div>
</template>
