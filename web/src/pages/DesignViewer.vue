<script setup lang="ts">
// Deliverable viewer page (/projects/:pid/deliverables/:id): finds the deliverable and its thread.
import { computed, watch } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { useDeliverables, useThreads } from "@/api/queries";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { useSeenDeliverables } from "@/lib/deliverableStrip";
import Viewer from "./design/Viewer.vue";

const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const list = useDeliverables(pid, "", "");
const threads = useThreads(pid);
const item = computed(() => list.data.value?.items.find((d) => d.id === route.params.id));
const thread = computed(() => threads.data.value?.find((t) => t.no === item.value?.thread_no));
// opening it here clears its "新内容" tag in the panel list's 交付物 strip (also when it updates while open)
const seen = useSeenDeliverables(() => pid.value);
watch(
  () => item.value && [item.value.id, item.value.updated_at],
  () => item.value && seen.markSeen(item.value),
  { immediate: true },
);
</script>

<template>
  <Loading v-if="list.isLoading.value" class="h-full" />
  <EmptyState v-else-if="!item" title="找不到这个交付物">
    <template #action>
      <RouterLink v-slot="{ href, navigate }" :to="`/projects/${pid}/deliverables`" custom>
        <el-button tag="a" :href="href" type="primary" @click="navigate">回到交付物</el-button>
      </RouterLink>
    </template>
  </EmptyState>
  <Viewer v-else :pid="pid" :item="item" :thread="thread" />
</template>
