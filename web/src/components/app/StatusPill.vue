<script setup lang="ts">
/** Outlined status pill (进行中 / 等你 / 待验收 …); in-progress pills lead with a spinner, a session's 已完成 / 已解决 with a check. */
import { Check, CheckCheck } from "lucide-vue-next";
import { cn } from "@/lib/utils";
import Spin from "./Spin.vue";

defineProps<{ tone: string; label?: string; class?: string; spin?: boolean; icon?: "check" | "resolved" }>();
</script>

<template>
  <span :class="cn('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs', $props.class)" :style="{ borderColor: tone, color: tone }" :data-icon="icon">
    <Spin v-if="spin" :color="tone" :size="10" />
    <Check v-else-if="icon === 'check'" class="-ml-0.5 size-3" :stroke-width="2.5" />
    <CheckCheck v-else-if="icon === 'resolved'" class="-ml-0.5 size-3" :stroke-width="2.5" />
    <slot>{{ label }}</slot>
  </span>
</template>
