<script setup lang="ts">
/** One block of a chat: a card, the user's bubble, a centred line, or a speaker's Markdown turn. */
import type { ThreadView } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import type { ChatBlock } from "@/lib/chat";
import { clock, cn } from "@/lib/utils";
import ChatCard from "./chatcards/ChatCard.vue";
import Dot from "./Dot.vue";
import MessageImages from "./MessageImages.vue";
import MessageRefs from "./composer/MessageRefs.vue";
import ThreadChip from "./ThreadChip.vue";

defineProps<{ block: ChatBlock; threads: ThreadView[]; speakerColor: string; compact?: boolean; pid: string }>();
const emit = defineEmits<{ openThread: [no: number] }>();
</script>

<template>
  <ChatCard v-if="block.kind === 'card'" :message="block.message" :pid="pid" />
  <div v-else-if="block.kind === 'user'" class="flex max-w-[85%] flex-col gap-2 self-end">
    <MessageImages :items="block.message.meta?.attachments" />
    <MessageRefs :refs="block.message.meta?.refs" />
    <div v-if="block.message.content" :class="cn('self-end whitespace-pre-wrap rounded-2xl bg-raised px-4 py-2.5', compact ? 'leading-6' : 'leading-[26px]')">{{ block.message.content }}</div>
  </div>
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
