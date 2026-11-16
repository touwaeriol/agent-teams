<script setup lang="ts">
/** Route wrapper: the settings shell for the signed-in user. */
import { useMe } from "@/api/queries";
import SettingsShell from "./SettingsShell.vue";

const me = useMe();
</script>

<template>
  <SettingsShell v-if="me.data.value" :me="me.data.value" />
</template>
