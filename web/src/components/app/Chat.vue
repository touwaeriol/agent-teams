<script setup lang="ts" generic="I extends { key: string; at: string }">
// The conversation column: messages (newest at the bottom, older pages on demand), chips for
// threads the speaker opened, chat cards (todos, plan, artifact, 会话已恢复), cards inserted
// where they were asked (`inserts` + the `insert` slot), and scrolling that follows new
// messages and comes back to where you were. Used by the coordinator chat and the thread panel.
import { computed, ref } from "vue";
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
    /** Items drawn between the messages at the time they happened (rendered by the `insert` slot). */
    inserts?: I[];
    /** Project id, for links from cards (deliverables). */
    pid?: string;
  }>(),
  { threads: () => [], speakerColor: "#C8622A", inserts: () => [], pid: "", filter: undefined },
);
const emit = defineEmits<{ openThread: [no: number] }>();
defineSlots<{ insert(p: { item: I }): unknown; empty(): unknown; after(): unknown }>();

const query = useMessages(() => props.cid);
const messages = computed(() => {
  const all = flattenMessages(query.data.value);
  return props.filter ? all.filter(props.filter) : all;
});
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
    <slot v-if="items.length === 0" name="empty" />
    <template v-for="it in items" :key="it.kind === 'block' ? it.block.message.id : it.key">
      <ChatBlockView
        v-if="it.kind === 'block'"
        :block="it.block"
        :threads="threads"
        :speaker-color="speakerColor"
        :compact="compact"
        :pid="pid"
        @open-thread="(no) => emit('openThread', no)"
      />
      <div v-else><slot name="insert" :item="it.node as I" /></div>
    </template>
    <slot name="after" />
    <div ref="end" />
  </div>
</template>
