<script setup lang="ts">
/** The ☰ button pages put in their header; only visible on phones. */
import { Menu } from "lucide-vue-next";
import { cn } from "@/lib/utils";
import { useShell } from "./shell";

defineProps<{ class?: string }>();
const { openNav } = useShell();
</script>

<template>
  <button type="button" aria-label="打开侧栏" :class="cn('-ml-2 flex size-11 items-center justify-center md:hidden', $props.class)" @click="openNav">
    <Menu class="size-5" :stroke-width="1.8" />
  </button>
</template>
