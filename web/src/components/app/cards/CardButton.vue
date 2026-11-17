<script setup lang="ts">
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import type { CardVariant } from "./variant";

/** `busy` shows a spinner and blocks clicks; `disabled` only blocks them (e.g. while another action runs). */
const props = defineProps<{ kind: "primary" | "outline" | "danger"; variant: CardVariant; busy?: boolean; disabled?: boolean }>();
const emit = defineEmits<{ click: [] }>();
const LOOK = { primary: "border-transparent bg-primary text-primary-foreground", outline: "border-input bg-white", danger: "border-[#EBC3C3] bg-white text-destructive" };
// The DOM attribute only updates on the next render; a second click in the same tick still lands.
const click = () => !props.busy && !props.disabled && emit("click");
</script>

<template>
  <button
    type="button"
    :disabled="busy || disabled"
    :aria-busy="busy || undefined"
    :class="cn('inline-flex items-center justify-center gap-1.5 rounded-lg border px-3 text-[13px] disabled:opacity-60', variant === 'mobile' ? 'h-11 rounded-[10px] text-sm' : 'h-8', LOOK[kind])"
    @click="click"
  >
    <Spinner v-if="busy" class="size-3.5" />
    <slot />
  </button>
</template>
