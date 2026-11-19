<script setup lang="ts" generic="I extends { key: string; at: string }">
// The conversation column: messages (newest at the bottom, older pages on demand), chips for
// threads the speaker opened, chat cards (todos, plan, artifact, 会话已恢复), cards inserted
// where they were asked (`inserts` + the `insert` slot), and scrolling that follows new
// messages and comes back to where you were. Used by the coordinator chat and the thread panel.
import { computed, ref, watch } from "vue";
import { pendingOf, prunePending, withPending } from "@/api/pending";
import { flattenMessages, useMessages } from "@/api/queries";
import type { MessageView, ThreadView } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import { chatBlocks, interleave } from "@/lib/chat";
import { cn } from "@/lib/utils";
import ChatBlockView from "./ChatBlockView.vue";
import { useChatScroll } from "./useChatScroll";

const props = withDefaults(
  defineProps<{
    cid: string | undefined;
    threads?: ThreadView[];
    speakerColor?: string;
    compact?: boolean;
    /** Messages to show (default: all). */
    filter?: (m: MessageView) => boolean;
    /** Messages to show, chosen from the whole list in order (applied after filter). */
    select?: (messages: MessageView[]) => MessageView[];
    /** Items drawn between the messages at the time they happened (rendered by the `insert` slot). */
    inserts?: I[];
    /** Project id, for links from cards (deliverables). */
    pid?: string;
    /** The user's bubbles can be forwarded to a thread (project chat). */
    forwardable?: boolean;
    /** Selecting messages to forward: the chosen ids. */
    selecting?: boolean;
    selectedIds?: number[];
  }>(),
  { threads: () => [], speakerColor: "#C8622A", inserts: () => [], pid: "", filter: undefined, select: undefined, selectedIds: () => [] },
);
const emit = defineEmits<{ openThread: [no: number]; forward: [id: number]; toggle: [id: number] }>();
defineSlots<{ insert(p: { item: I }): unknown; empty(): unknown; before(): unknown; after(): unknown }>();

const query = useMessages(() => props.cid);
const messages = computed(() => {
  // The user's own messages show at once, before the server confirms them (api/pending.ts).
  const all = withPending(flattenMessages(query.data.value), pendingOf(props.cid));
  const shown = props.filter ? all.filter(props.filter) : all;
  return props.select ? props.select(shown) : shown;
});
// A send delivered before the messages had loaded is drawn from api/pending.ts until they hold it.
watch(
  () => query.data.value,
  (data) => prunePending(props.cid, flattenMessages(data)),
);
const items = computed(() => interleave(chatBlocks(messages.value), props.inserts.map((i) => ({ key: i.key, at: i.at, node: i }))));
const end = ref<HTMLElement | null>(null);
useChatScroll(() => props.cid, end, () => messages.value.length + props.inserts.length, () => !query.isLoading.value);
</script>

<template>
  <Loading v-if="query.isLoading.value" class="py-16" />
  <div v-else :class="cn('flex flex-col', compact ? 'gap-[18px]' : 'gap-7')">
    <button v-if="query.hasNextPage.value" type="button" class="self-center text-xs text-muted-foreground hover:text-foreground" @click="query.fetchNextPage()">
      {{ query.isFetchingNextPage.value ? "加载中…" : "更早的消息" }}
    </button>
    <!-- What came before the first message (a thread's brief): only once the oldest page is loaded. -->
    <slot v-if="!query.hasNextPage.value" name="before" />
    <slot v-if="items.length === 0" name="empty" />
    <template v-for="it in items" :key="it.kind === 'block' ? it.block.message.id : it.key">
      <ChatBlockView
        v-if="it.kind === 'block'"
        :block="it.block"
        :threads="threads"
        :speaker-color="speakerColor"
        :compact="compact"
        :pid="pid"
        :forwardable="forwardable"
        :selecting="selecting && it.block.kind === 'user' && !it.block.message.local"
        :selected="selectedIds.includes(Number(it.block.message.id))"
        @open-thread="(no) => emit('openThread', no)"
        @forward="(id) => emit('forward', id)"
        @toggle="(id) => emit('toggle', id)"
      />
      <div v-else><slot name="insert" :item="it.node as I" /></div>
    </template>
    <slot name="after" />
    <div ref="end" />
  </div>
</template>
