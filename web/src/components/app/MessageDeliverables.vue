<script setup lang="ts">
/**
 * Cards for the deliverables a message mentions by id ("交付物 dl_01m8…"), drawn under its text.
 * The ids are looked up through the project's deliverable list (the server only returns this
 * project's, for its owner): unknown or foreign ids get no card and stay plain text.
 */
import { computed } from "vue";
import { useDeliverablesByIds } from "@/api/queries";
import { deliverableIdsIn, mentionedDeliverables } from "@/lib/deliverableRefs";
import DeliverableChip from "./DeliverableChip.vue";

const props = defineProps<{ pid: string; source: string }>();
const ids = computed(() => (props.pid ? deliverableIdsIn(props.source) : []));
const query = useDeliverablesByIds(() => props.pid, ids);
const items = computed(() => (ids.value.length ? mentionedDeliverables(ids.value, query.data.value?.items) : []));
</script>

<template>
  <div v-if="items.length" class="flex flex-col gap-2" data-message-deliverables>
    <DeliverableChip v-for="d in items" :key="d.id" :pid="pid" :item="d" />
  </div>
</template>
