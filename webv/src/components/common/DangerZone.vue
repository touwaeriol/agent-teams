<script setup lang="ts">
/** A danger-zone row: explanation on the left, the confirmed action (default slot) on the right. */
defineProps<{ title: string; description?: string }>();
</script>

<template>
  <section class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-panel p-5">
    <div>
      <h2 class="font-semibold">{{ title }}</h2>
      <p v-if="description" class="text-[13px] text-muted-foreground">{{ description }}</p>
    </div>
    <slot />
  </section>
</template>
