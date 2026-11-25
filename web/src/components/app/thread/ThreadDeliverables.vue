<script setup lang="ts">
/**
 * At the end of a finished thread's chat: every deliverable the thread published, the lead's and
 * the members' alike (by thread, not by publisher), so the final report links them all.
 */
import { computed } from "vue";
import { useThreadDeliverables } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { isLibraryDeliverable } from "@/lib/library";
import DeliverableChip from "../DeliverableChip.vue";

const props = defineProps<{ pid: string; thread: ThreadView }>();
const list = useThreadDeliverables(() => props.pid, () => props.thread.no);
const items = computed(() => (list.data.value?.items ?? []).filter((d) => d.kind !== "pr" && isLibraryDeliverable(d)));
</script>

<template>
  <section v-if="items.length" class="flex flex-col gap-2" aria-label="本讨论串的交付物" data-thread-deliverables>
    <span class="text-xs text-muted-foreground">本讨论串的交付物 · {{ items.length }}</span>
    <DeliverableChip v-for="d in items" :key="d.id" :pid="pid" :item="d" />
  </section>
</template>
