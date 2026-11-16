<script setup lang="ts">
/** One skill: monospace name, a badge (内置 / size / who published it), two lines of description. */
defineProps<{ name: string; badge: string; description: string }>();
</script>

<template>
  <div class="flex items-start justify-between gap-2 rounded-[10px] border px-3 py-2.5">
    <span class="flex min-w-0 flex-col gap-0.5">
      <span class="flex items-center gap-2 text-[13px] font-medium">
        <span class="font-mono">{{ name }}</span>
        <span class="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">{{ badge }}</span>
      </span>
      <span class="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{{ description }}</span>
    </span>
    <slot />
  </div>
</template>
