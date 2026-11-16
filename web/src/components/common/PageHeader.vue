<script setup lang="ts">
defineProps<{ title?: string; description?: string }>();
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div class="flex min-w-0 flex-col gap-1">
      <h1 class="text-[22px] font-semibold tracking-tight"><slot name="title">{{ title }}</slot></h1>
      <p v-if="description || $slots.description" class="max-w-3xl text-[13px] leading-5 text-muted-foreground">
        <slot name="description">{{ description }}</slot>
      </p>
    </div>
    <div v-if="$slots.actions" class="flex flex-wrap gap-2"><slot name="actions" /></div>
  </div>
</template>
