<script setup lang="ts">
/** Route wrapper: the project shell for the signed-in user. */
import { useMe } from "@/api/queries";
import ProjectShell from "./ProjectShell.vue";

const me = useMe();
</script>

<template>
  <ProjectShell v-if="me.data.value" :me="me.data.value" />
</template>
