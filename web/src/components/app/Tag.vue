<script setup lang="ts">
/** Grey tag (branch, worktree, node…). */
import { cn } from "@/lib/utils";

defineProps<{ mono?: boolean; class?: string }>();
</script>

<template>
  <span :class="cn('rounded-md bg-sidebar px-2 py-[3px] text-xs text-[#4A4843]', mono && 'font-mono', $props.class)"><slot /></span>
</template>
