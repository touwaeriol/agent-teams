<script setup lang="ts">
// A compact 32px icon button whose label shows as a tooltip (and is its accessible name).
// With `to` it is a router link of the same look.
import { RouterLink } from "vue-router";
import { cn } from "@/lib/utils";

defineOptions({ inheritAttrs: false });
defineProps<{ label: string; active?: boolean; to?: string; class?: string }>();
const ICON =
  "flex size-8 shrink-0 items-center justify-center rounded-lg text-[#4A4843] transition-colors hover:bg-raised dark:text-foreground/80 [&_svg]:size-[17px] aria-expanded:bg-raised";
</script>

<template>
  <el-tooltip :content="label" placement="bottom" :show-after="300" :hide-after="0" effect="dark" :show-arrow="false" :offset="6">
    <RouterLink v-if="to" :to="to" :aria-label="label" :class="cn(ICON, $props.class)"><slot /></RouterLink>
    <button v-else type="button" :aria-label="label" :aria-pressed="active" :class="cn(ICON, active && 'bg-raised text-foreground', $props.class)" v-bind="$attrs">
      <slot />
    </button>
  </el-tooltip>
</template>
