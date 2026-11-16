<script setup lang="ts">
/** A deliverable's link: in-app viewer (router link) or the external URL in a new tab. */
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { DeliverableView } from "@/api/views";
import { deliverableHref } from "@/lib/library";

const props = defineProps<{ pid: string; item: DeliverableView; class?: string }>();
const target = computed(() => deliverableHref(props.pid, props.item));
</script>

<template>
  <RouterLink v-if="target.to" :to="target.to" :class="props.class"><slot /></RouterLink>
  <a v-else :href="target.href" target="_blank" rel="noreferrer" :class="props.class"><slot /></a>
</template>
