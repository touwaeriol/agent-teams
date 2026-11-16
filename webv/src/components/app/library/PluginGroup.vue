<script setup lang="ts">
defineProps<{ title: string; empty: string; count: number }>();
</script>

<template>
  <div class="flex flex-col gap-2">
    <h3 class="text-[13px] font-semibold text-muted-foreground">{{ title }}</h3>
    <p v-if="count === 0" class="text-xs text-muted-foreground">{{ empty }}</p>
    <div v-else class="grid gap-2 sm:grid-cols-2"><slot /></div>
  </div>
</template>
