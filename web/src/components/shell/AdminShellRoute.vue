<script setup lang="ts">
/** Route wrapper: the admin console shell. The router guard already turns non-admins away (404);
 * the role check here is a second line so nothing admin-only renders for them. */
import { useMe } from "@/api/queries";
import AdminShell from "./AdminShell.vue";

const me = useMe();
</script>

<template>
  <AdminShell v-if="me.data.value?.role === 'admin'" :me="me.data.value" />
</template>
