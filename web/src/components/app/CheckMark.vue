<script setup lang="ts">
import { $t } from "@/i18n";
/** The checklist mark of a main task: filled tick, spinning arc, dashed circle. */
import type { MainTaskStatus } from "@/api/views";

withDefaults(defineProps<{ status: MainTaskStatus; size?: number }>(), { size: 15 });
</script>

<template>
  <svg v-if="status === 'done'" :width="size" :height="size" viewBox="0 0 16 16" :aria-label="$t('已完成')" class="shrink-0">
    <circle cx="8" cy="8" r="8" fill="#8A877F" />
    <path d="M4.6 8.2l2.2 2.2 4.6-4.8" fill="none" stroke="#FFFFFF" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
  <svg v-else-if="status === 'doing'" :width="size" :height="size" viewBox="0 0 16 16" role="img" :aria-label="$t('进行中')" data-spin="" class="at-spin shrink-0">
    <circle cx="8" cy="8" r="6.5" fill="none" stroke="#E7E5E0" stroke-width="1.6" />
    <path d="M8 1.5a6.5 6.5 0 0 1 6.5 6.5" fill="none" stroke="#4A4843" stroke-width="1.6" stroke-linecap="round" />
  </svg>
  <svg v-else :width="size" :height="size" viewBox="0 0 16 16" :aria-label="$t('未开始')" class="shrink-0">
    <circle cx="8" cy="8" r="6.5" fill="none" stroke="#B7B3AA" stroke-width="1.4" stroke-dasharray="2 2.4" />
  </svg>
</template>
