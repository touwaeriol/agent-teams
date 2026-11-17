<script setup lang="ts">
/**
 * You and the lead: the brief the thread was opened with first, then the lead's messages as
 * Markdown, yours as bubbles, open cards where they were asked. Until the lead's first message
 * a quiet "组长正在阅读任务…" sits under the brief.
 */
import { computed } from "vue";
import { flattenMessages, useMessages, useQuestions } from "@/api/queries";
import type { QuestionView, ThreadView } from "@/api/views";
import { chatQuestions } from "@/lib/ask";
import { threadChat } from "@/lib/chat";
import { threadActive } from "@/lib/live";
import { leadOf, showReviewCard } from "@/lib/present";
import Chat from "../Chat.vue";
import ReviewCard from "../cards/ReviewCard.vue";
import WaitingCard from "../cards/WaitingCard.vue";
import Spin from "../Spin.vue";
import { leadReading } from "./detail";
import DispatchMessage from "./DispatchMessage.vue";
import TypingLine from "./TypingLine.vue";

const props = defineProps<{ pid: string; thread: ThreadView; compact: boolean }>();
const questions = useQuestions(() => props.thread.id);
const lead = computed(() => leadOf(props.thread)?.name);
const inserts = computed(() => chatQuestions(questions.data.value).map((q: QuestionView) => ({ key: q.id, at: q.created_at, question: q })));
// Same query as the chat's (shared cache): has the lead said anything yet?
const messages = useMessages(() => props.thread.id);
const reading = computed(
  () => !messages.isLoading.value && !["done", "failed", "archived"].includes(props.thread.status) && leadReading(flattenMessages(messages.data.value), messages.hasNextPage.value),
);
// While the lead reads, the reading line stands in for "正在唤醒组长 / 组长正在回复"; queued and paused still say so.
const typing = computed(() => !reading.value || props.thread.status === "queued" || props.thread.status === "paused");
</script>

<template>
  <Chat :cid="thread.id" :pid="pid" :select="threadChat" :inserts="inserts" :compact="compact" speaker-color="#2F6FDB">
    <template #before><DispatchMessage v-if="thread.dispatch" :dispatch="thread.dispatch" :pid="pid" :compact="compact" /></template>
    <template #insert="{ item }"><WaitingCard :cid="thread.id" :pid="pid" :question="item.question" variant="soft" :lead-name="lead" /></template>
    <template #after>
      <p v-if="reading" role="status" class="flex items-center gap-2 text-[13px] text-muted-foreground" data-lead-reading>
        <Spin v-if="!typing && threadActive(thread)" color="#8A877F" :size="11" />
        组长正在阅读任务…
      </p>
      <ReviewCard v-if="showReviewCard(thread)" :thread="thread" variant="soft" />
      <TypingLine v-if="typing" :thread="thread" />
    </template>
  </Chat>
</template>
