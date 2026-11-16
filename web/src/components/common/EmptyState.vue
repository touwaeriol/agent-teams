<script setup lang="ts">
/** Nothing to show yet, with an optional next step (action slot). */
defineProps<{ title: string; description?: string }>();
</script>

<template>
  <div class="flex flex-col items-center gap-3 rounded-xl border border-dashed bg-panel/50 px-6 py-12 text-center">
    <div v-if="$slots.icon" class="text-muted-foreground [&_svg]:size-8"><slot name="icon" /></div>
    <div class="text-[15px] font-semibold">{{ title }}</div>
    <p v-if="description || $slots.description" class="max-w-md text-[13px] leading-5 text-muted-foreground"><slot name="description">{{ description }}</slot></p>
    <slot name="action" />
  </div>
</template>
