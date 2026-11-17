<script setup lang="ts">
import { useUserEvents } from "@/api/sse";
import type { UserView } from "@/api/views";
import AdminNav from "./AdminNav.vue";
import ShellFrame from "./ShellFrame.vue";

defineProps<{ me: UserView }>();
useUserEvents(true);
</script>

<template>
  <ShellFrame>
    <template #nav="{ close }"><AdminNav :me="me" @navigate="close?.()" /></template>
  </ShellFrame>
</template>
