<script setup lang="ts">
import { useUserEvents } from "@/api/sse";
import type { UserView } from "@/api/views";
import SettingsNav from "./SettingsNav.vue";
import ShellFrame from "./ShellFrame.vue";

defineProps<{ me: UserView }>();
useUserEvents(true);
</script>

<template>
  <ShellFrame>
    <template #nav="{ close }"><SettingsNav :me="me" @navigate="close?.()" /></template>
  </ShellFrame>
</template>
