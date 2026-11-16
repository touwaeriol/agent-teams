<script setup lang="ts">
/** A library card on the Team / Admin pages: title, help text, upload buttons, then the rows. */
defineProps<{ title: string; help: string; label?: string }>();
</script>

<template>
  <section class="flex flex-col gap-3 rounded-[14px] border bg-panel p-[18px]" :aria-label="label">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="flex flex-col gap-1">
        <h2 class="text-base font-semibold">{{ title }}</h2>
        <p class="max-w-[620px] text-[13px] leading-[21px] text-muted-foreground">{{ help }}</p>
      </div>
      <div class="flex gap-2"><slot name="actions" /></div>
    </div>
    <slot />
  </section>
</template>
