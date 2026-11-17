<script setup lang="ts">
/** 交付物: the thread's PRs (open one when the main tasks are done), merge queue, CI, then published deliverables. */
import { computed } from "vue";
import { useDeliverables } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import { isLibraryDeliverable } from "@/lib/library";
import DeliverableCard from "./DeliverableCard.vue";
import Delivery from "./Delivery.vue";

const props = defineProps<{ pid: string; thread: ThreadView }>();
const deliverables = useDeliverables(() => props.pid, "", "", true);
const mine = computed(() => (deliverables.data.value?.items ?? []).filter((d) => d.thread_no === props.thread.no && isLibraryDeliverable(d)));
</script>

<template>
  <Delivery :thread="thread" />
  <section class="flex flex-col gap-2">
    <span class="text-xs text-muted-foreground">设计稿、文档和文件</span>
    <Loading v-if="deliverables.isLoading.value" class="py-6" />
    <p v-else-if="mine.length === 0" class="text-[13px] leading-6 text-muted-foreground">还没有。员工发布的设计稿、文档和文件会出现在这里。</p>
    <div v-else class="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
      <DeliverableCard v-for="d in mine" :key="d.id" :pid="pid" :item="d" />
    </div>
  </section>
</template>
