<script setup lang="ts">
/** A question for the user. Open: pick or write 其他, then submit. Answered: the question with the chosen rows ticked. */
import { computed } from "vue";
import type { QuestionView } from "@/api/views";
import { askItems } from "@/lib/ask";
import AnsweredAsk from "./AnsweredAsk.vue";
import OpenAsk from "./OpenAsk.vue";

const props = defineProps<{ cid: string; pid: string; question: QuestionView; who?: string }>();
const items = computed(() => askItems(props.question));
</script>

<template>
  <OpenAsk v-if="question.status === 'open'" :key="question.id" :cid="cid" :pid="pid" :question="question" :items="items" :who="who" />
  <AnsweredAsk v-else :question="question" :items="items" :who="who" />
</template>
