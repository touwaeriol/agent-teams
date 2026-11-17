<script setup lang="ts">
/**
 * One block of a chat: a card, the user's bubble, a centred line, or a speaker's Markdown turn.
 * In the project chat the user's bubbles can be forwarded to a thread (hover action, or a
 * round check while selecting); a forwarded copy in a thread chat ends with a grey line that
 * links back to the project chat.
 */
import { Check, Forward } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink, useRoute } from "vue-router";
import type { ThreadView } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import type { ChatBlock } from "@/lib/chat";
import { backLink, forwardLine, messageAnchor } from "@/lib/forward";
import { clock, cn } from "@/lib/utils";
import ChatCard from "./chatcards/ChatCard.vue";
import Dot from "./Dot.vue";
import MessageImages from "./MessageImages.vue";
import MessageRefs from "./composer/MessageRefs.vue";
import ThreadChip from "./ThreadChip.vue";

const props = defineProps<{
  block: ChatBlock;
  threads: ThreadView[];
  speakerColor: string;
  compact?: boolean;
  pid: string;
  /** The user's bubbles offer "转发到讨论串" (project chat). */
  forwardable?: boolean;
  /** Selection mode: bubbles toggle instead. */
  selecting?: boolean;
  selected?: boolean;
}>();
const emit = defineEmits<{ openThread: [no: number]; forward: [id: number]; toggle: [id: number] }>();
const route = useRoute();
const id = computed(() => Number(props.block.message.id));
const line = computed(() => (props.block.kind === "user" ? forwardLine(props.block.message) : null));
const back = computed(() => (line.value ? backLink(route.path, props.pid, line.value.anchor) : null));
const forwardedTo = computed(() => props.block.message.meta?.forwarded_to);
const pick = () => props.selecting && emit("toggle", id.value);
</script>

<template>
  <ChatCard v-if="block.kind === 'card'" :message="block.message" :pid="pid" />
  <div
    v-else-if="block.kind === 'user'"
    :id="messageAnchor(block.message.id)"
    :class="cn('group/msg flex items-start gap-3 rounded-2xl transition-colors', selecting ? 'cursor-pointer self-stretch' : 'self-end max-w-[85%]', selected && 'bg-primary/5')"
    :data-selected="selecting ? String(Boolean(selected)) : undefined"
    @click="pick"
  >
    <button
      v-if="selecting"
      type="button"
      role="checkbox"
      :aria-checked="Boolean(selected)"
      aria-label="选择这条消息"
      :class="cn('mt-2.5 flex size-5 shrink-0 items-center justify-center rounded-full border-[1.5px] transition-colors', selected ? 'border-primary bg-primary text-primary-foreground' : 'border-input bg-panel')"
      @click.stop="pick"
    >
      <Check v-if="selected" class="size-3" :stroke-width="3" />
    </button>
    <button
      v-else-if="forwardable"
      type="button"
      aria-label="转发到讨论串"
      title="转发到讨论串"
      class="mt-1.5 flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground opacity-0 transition-opacity hover:bg-raised hover:text-foreground focus-visible:opacity-100 group-hover/msg:opacity-100"
      @click.stop="emit('forward', id)"
    >
      <Forward class="size-4" :stroke-width="1.8" />
    </button>
    <div :class="cn('flex min-w-0 flex-col gap-2', selecting && 'ml-auto max-w-[85%]')">
      <MessageImages :items="block.message.meta?.attachments" />
      <MessageRefs :refs="block.message.meta?.refs" />
      <div v-if="block.message.content" :class="cn('self-end whitespace-pre-wrap rounded-2xl bg-raised px-4 py-2.5', compact ? 'leading-6' : 'leading-[26px]')">{{ block.message.content }}</div>
      <p v-if="line && back" class="self-end text-right text-xs text-muted-foreground" data-forward-line>
        <RouterLink :to="back" class="hover:text-foreground hover:underline">{{ line.text }}</RouterLink>
        <span v-if="line.note" class="block">附言：{{ line.note }}</span>
      </p>
    </div>
  </div>
  <p v-else-if="block.kind === 'line' && forwardedTo" class="text-center text-xs text-muted-foreground">
    <button type="button" class="hover:text-foreground hover:underline" @click="emit('openThread', forwardedTo.no)">{{ block.message.content }}</button>
  </p>
  <p v-else-if="block.kind === 'line'" :class="cn('text-center text-xs', block.message.role === 'error' ? 'text-destructive' : 'text-muted-foreground')">{{ block.message.content }}</p>
  <article v-else class="flex flex-col gap-2.5">
    <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
      <Dot :color="speakerColor" :size="8" />
      {{ block.message.author || "leader" }} · {{ clock(block.message.created_at) }}
      <span v-if="block.message.kind === 'interrupt'" class="text-[#B4530B]">· 插话</span>
    </span>
    <Markdown :source="block.message.content" :class="compact ? 'text-sm leading-[26px]' : 'text-[15px] leading-7'" />
    <ThreadChip v-for="no in block.opens" :key="no" :thread="threads.find((t) => t.no === no)" :no="no" @open="(n) => emit('openThread', n)" />
  </article>
</template>
