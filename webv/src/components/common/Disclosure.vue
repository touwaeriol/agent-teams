<script setup lang="ts">
/** Advanced options folded away by default. */
defineProps<{ summary: string; defaultOpen?: boolean }>();
</script>

<template>
  <details class="group rounded-lg border bg-raised/40 p-3" :open="defaultOpen">
    <summary class="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{{ summary }}</summary>
    <div class="mt-3 flex flex-col gap-4"><slot /></div>
  </details>
</template>
