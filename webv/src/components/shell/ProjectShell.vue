<script setup lang="ts">
/** Project pages: the project sidebar, plus the user's and the project's live event streams. */
import { computed } from "vue";
import { useRoute } from "vue-router";
import { useProjectEvents, useUserEvents } from "@/api/sse";
import type { UserView } from "@/api/views";
import ProjectSidebar from "./ProjectSidebar.vue";
import ShellFrame from "./ShellFrame.vue";

defineProps<{ me: UserView }>();
const route = useRoute();
const pid = computed(() => (typeof route.params.pid === "string" ? route.params.pid : undefined));
useUserEvents(true);
useProjectEvents(pid);
</script>

<template>
  <ShellFrame>
    <template #nav="{ close }"><ProjectSidebar :me="me" @navigate="close?.()" /></template>
  </ShellFrame>
</template>
