<script setup lang="ts">
/** The card for a waiting question: a plan approval, a permission request, or a question (AskCard). */
import type { QuestionView } from "@/api/views";
import AskCard from "../chatcards/AskCard.vue";
import ApprovalCard from "./ApprovalCard.vue";
import PlanApprovalCard from "./PlanApprovalCard.vue";
import { askerName, type CardVariant } from "./variant";

withDefaults(defineProps<{ cid: string; pid: string; question: QuestionView; variant?: CardVariant; leadName?: string }>(), { variant: "soft", leadName: undefined });
</script>

<template>
  <PlanApprovalCard v-if="question.kind === 'approval' && question.detail.tool_name === 'ExitPlanMode'" :cid="cid" :pid="pid" :question="question" :variant="variant" />
  <ApprovalCard v-else-if="question.kind === 'approval'" :cid="cid" :pid="pid" :question="question" :variant="variant" />
  <AskCard v-else :cid="cid" :pid="pid" :question="question" :who="askerName(question, leadName)" />
</template>
