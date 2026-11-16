<script setup lang="ts">
/** The right panel with one thread open (or a note when it is gone). */
import { computed } from "vue";
import { useWaiting } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ThreadDetail from "@/components/app/thread/ThreadDetail.vue";
import type { ThreadViewId } from "@/components/app/threadMenu";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { threadNav } from "@/components/app/thread/detail";
import { listOrder, loadFolded } from "@/lib/threadList";
import { listFilterOf, listView } from "@/lib/threadListView";
import { threadPath, waitingIndex } from "./logic";

const props = defineProps<{ pid: string; no: number; view: ThreadViewId | null; threads: ThreadView[]; loading: boolean; userId: string }>();
const emit = defineEmits<{ view: [view: ThreadViewId | null]; back: []; close: [] }>();
const thread = computed(() => props.threads.find((t) => t.no === props.no));
const full = computed(() => `/projects/${props.pid}/threads/${props.no}/full`);
const waiting = useWaiting(() => props.pid);
/** 上一个 / 下一个讨论串: the list as the panel shows it (groups, search, filters, folds). */
const nav = computed(() => {
  const order = listOrder(props.threads, props.no, listFilterOf(listView(props.pid)), waitingIndex(waiting.data.value?.questions), loadFolded(props.userId));
  return threadNav(order, props.no, (n) => threadPath(props.pid, n));
});
</script>

<template>
  <Loading v-if="loading" class="py-16" />
  <EmptyState v-else-if="!thread" :title="`找不到讨论串 #${no}`" description="它可能已被删除。">
    <template #action>
      <button type="button" class="text-[13px] underline-offset-2 hover:underline" @click="emit('back')">回到讨论串列表</button>
    </template>
  </EmptyState>
  <ThreadDetail
    v-else
    :key="thread.id"
    :pid="pid"
    :thread="thread"
    :view="view"
    :full-href="full"
    :nav="nav"
    :expand="{ to: full, label: '全屏打开' }"
    @view="(v) => emit('view', v)"
    @back="emit('back')"
    @close="emit('close')"
  />
</template>
