<script setup lang="ts">
// App shell: a fixed sidebar on desktop, a drawer on phones. The `nav` slot gets `{ close }`
// (undefined for the desktop copy). Pages open the drawer with useShell().openNav.
import { ref, watch } from "vue";
import { RouterView, useRoute } from "vue-router";
import { cn } from "@/lib/utils";
import { provideShell } from "./shell";

const open = ref(false);
const route = useRoute();
watch(() => route.path, () => (open.value = false));
provideShell({ openNav: () => (open.value = true) });
const close = () => (open.value = false);
</script>

<template>
  <div class="flex h-full min-h-0">
    <div class="hidden h-full md:block"><slot name="nav" :close="undefined" /></div>
    <div :class="cn('fixed inset-0 z-40 md:hidden', open ? '' : 'pointer-events-none')" :aria-hidden="!open">
      <div :class="cn('absolute inset-0 bg-black/30 transition-opacity', open ? 'opacity-100' : 'opacity-0')" @click="close" />
      <div :class="cn('absolute inset-y-0 left-0 transition-transform', open ? 'translate-x-0' : '-translate-x-full')"><slot name="nav" :close="close" /></div>
    </div>
    <div class="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto"><RouterView /></div>
  </div>
</template>
