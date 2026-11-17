<script setup lang="ts">
/**
 * You and the lead: the brief the thread was opened with first, then the lead's messages as
 * Markdown, yours as bubbles, open cards where they were asked. While the thread is queued or
 * running and the lead has neither spoken nor asked yet, a quiet "组长正在阅读任务…" sits under it.
 * Older threads with no brief and no messages keep the plain empty state.
 */
import { computed } from "vue";
import { flattenMessages, useMessages, useQuestions } from "@/api/queries";
import type { QuestionView, ThreadView } from "@/api/views";
import { chatQuestions } from "@/lib/ask";
import { threadChat } from "@/lib/chat";
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
// Only while the thread is queued or running, before the lead's first message or question card.
const reading = computed(
  () =>
    (props.thread.status === "queued" || props.thread.status === "running") &&
    !messages.isLoading.value &&
    inserts.value.length === 0 &&
    leadReading(flattenMessages(messages.data.value), messages.hasNextPage.value),
);
// While the lead reads, the reading line stands in for "正在唤醒组长 / 组长正在回复"; a queued thread still says so.
const typing = computed(() => !reading.value || props.thread.status === "queued");
const dispatched = computed(() => (props.thread.dispatch && props.thread.brief.trim() ? props.thread.dispatch : null));
</script>

<template>
  <Chat :cid="thread.id" :pid="pid" :select="threadChat" :inserts="inserts" :compact="compact" speaker-color="#2F6FDB">
    <template #before><DispatchMessage v-if="dispatched" :brief="thread.brief" :dispatch="dispatched" :pid="pid" :compact="compact" /></template>
    <template #insert="{ item }"><WaitingCard :cid="thread.id" :pid="pid" :question="item.question" variant="soft" :lead-name="lead" /></template>
    <template #empty>
      <p v-if="!dispatched && !reading" class="py-10 text-center text-[13px] leading-6 text-muted-foreground">还没有对话。组长汇报会出现在这里，你也可以直接给组长发消息。</p>
    </template>
    <template #after>
      <p v-if="reading" role="status" class="flex items-center gap-2 text-[13px] text-muted-foreground" data-lead-reading>
        <Spin v-if="!typing" color="#8A877F" :size="11" />
        组长正在阅读任务…
      </p>
      <ReviewCard v-if="showReviewCard(thread)" :thread="thread" variant="soft" />
      <TypingLine v-if="typing" :thread="thread" />
    </template>
  </Chat>
</template>
