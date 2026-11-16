<script setup lang="ts">
/** You and the lead: the lead's messages as Markdown, yours as bubbles, open cards where they were asked. */
import { computed } from "vue";
import { useQuestions } from "@/api/queries";
import type { QuestionView, ThreadView } from "@/api/views";
import { chatQuestions } from "@/lib/ask";
import { threadTalk } from "@/lib/chat";
import { leadOf } from "@/lib/present";
import Chat from "../Chat.vue";
import ReviewCard from "../cards/ReviewCard.vue";
import WaitingCard from "../cards/WaitingCard.vue";
import TypingLine from "./TypingLine.vue";

const props = defineProps<{ pid: string; thread: ThreadView; compact: boolean }>();
const questions = useQuestions(() => props.thread.id);
const lead = computed(() => leadOf(props.thread)?.name);
const inserts = computed(() => chatQuestions(questions.data.value).map((q: QuestionView) => ({ key: q.id, at: q.created_at, question: q })));
</script>

<template>
  <Chat :cid="thread.id" :pid="pid" :filter="threadTalk" :inserts="inserts" :compact="compact" speaker-color="#2F6FDB">
    <template #insert="{ item }"><WaitingCard :cid="thread.id" :pid="pid" :question="item.question" variant="soft" :lead-name="lead" /></template>
    <template #empty>
      <p class="py-10 text-center text-[13px] leading-6 text-muted-foreground">还没有对话。组长汇报会出现在这里，你也可以直接给组长发消息。</p>
    </template>
    <template #after>
      <ReviewCard v-if="thread.status === 'review'" :thread="thread" variant="soft" />
      <TypingLine :thread="thread" />
    </template>
  </Chat>
</template>
