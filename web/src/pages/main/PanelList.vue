<script setup lang="ts">
import { computed } from "vue";
import { useWaiting } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ThreadList from "@/components/app/ThreadList.vue";
import Loading from "@/components/common/Loading.vue";
import { waitingIndex } from "./logic";

const props = defineProps<{ pid: string; threads: ThreadView[]; loading: boolean }>();
const emit = defineEmits<{ open: [no: number] }>();
const waiting = useWaiting(() => props.pid);
const visible = computed(() => props.threads.filter((t) => t.status !== "archived").length);
const index = computed(() => waitingIndex(waiting.data.value?.questions));
</script>

<template>
  <div class="flex h-14 shrink-0 items-center justify-between border-b border-[#EFEDE8] px-5">
    <nav aria-label="位置" class="font-semibold">讨论串</nav>
    <span class="text-xs text-muted-foreground">共 {{ visible }} 个</span>
  </div>
  <div class="scroll-thin flex-1 overflow-y-auto px-5 pb-6 pt-3.5">
    <Loading v-if="loading" />
    <ThreadList v-else :threads="threads" :waiting-by-thread="index" menus @open="(no) => emit('open', no)" />
  </div>
</template>
