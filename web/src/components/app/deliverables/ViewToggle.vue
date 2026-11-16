<script setup lang="ts">
import { Grid2x2, List } from "lucide-vue-next";
import { cn } from "@/lib/utils";
import type { LibraryView } from "./browser";

defineProps<{ value: LibraryView }>();
const emit = defineEmits<{ change: [value: LibraryView] }>();
const cls = (on: boolean) => cn("flex size-7 items-center justify-center rounded-md", on ? "bg-panel shadow-sm" : "text-muted-foreground hover:text-foreground");
</script>

<template>
  <span role="group" aria-label="显示方式" class="flex items-center gap-0.5 rounded-lg bg-raised p-0.5">
    <button type="button" aria-label="网格" :aria-pressed="value === 'grid'" :class="cls(value === 'grid')" @click="emit('change', 'grid')">
      <Grid2x2 class="size-4" :stroke-width="1.8" />
    </button>
    <button type="button" aria-label="列表" :aria-pressed="value === 'list'" :class="cls(value === 'list')" @click="emit('change', 'list')">
      <List class="size-4" :stroke-width="1.8" />
    </button>
  </span>
</template>
