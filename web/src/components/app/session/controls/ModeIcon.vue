<script setup lang="ts">
/** The icon of a permission mode (MODE_LOOK.icon). */
import { Hand, ListChecks, PenLine, ShieldOff, Sparkles } from "lucide-vue-next";
import { computed } from "vue";
import { MODE_LOOK, isMode } from "@/lib/sessionControls";

const props = defineProps<{ mode: string }>();
const ICONS = { sparkles: Sparkles, hand: Hand, pen: PenLine, list: ListChecks, shield: ShieldOff };
const icon = computed(() => (isMode(props.mode) ? ICONS[MODE_LOOK[props.mode].icon] : Hand));
</script>

<template>
  <component :is="icon" aria-hidden="true" />
</template>
