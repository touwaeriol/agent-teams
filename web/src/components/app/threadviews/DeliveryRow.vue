<script setup lang="ts">
defineProps<{ branch: string; what: string; look: { tone: string; label: string }; href?: string }>();
</script>

<template>
  <span class="flex flex-wrap items-center gap-x-3.5 gap-y-1 rounded-lg bg-background px-2.5 py-2 text-[13px]">
    <span class="min-w-0 flex-[1_1_220px] font-mono [overflow-wrap:anywhere]">{{ branch }}</span>
    <span class="text-muted-foreground">{{ what }}</span>
    <a v-if="href" :href="href" target="_blank" rel="noreferrer" :style="{ color: look.tone }">{{ look.label }} ↗</a>
    <span v-else :style="{ color: look.tone }">{{ look.label }}</span>
  </span>
</template>
