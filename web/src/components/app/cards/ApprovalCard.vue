<script setup lang="ts">
/** A permission request: 放行 / 总是允许 / 拒绝 (with an optional reason sent to the employee). */
import { computed, ref } from "vue";
import type { QuestionView } from "@/api/views";
import { approvalCardStatus, approvalCommand, reviewNote } from "@/lib/present";
import CardActions from "./CardActions.vue";
import CardButton from "./CardButton.vue";
import CardFrame from "./CardFrame.vue";
import ReasonBox from "./ReasonBox.vue";
import { useAnswer } from "./useAnswer";
import type { CardVariant } from "./variant";

const props = withDefaults(defineProps<{ cid: string; pid: string; question: QuestionView; variant?: CardVariant }>(), { variant: "soft" });
const answer = useAnswer(() => props.cid, () => props.question, () => props.pid);
const denying = ref(false);
const note = computed(() => reviewNote(props.question));
const pending = (a: string) => answer.isPending.value && answer.variables.value?.answer === a;
</script>

<template>
  <CardFrame :variant="variant" :code="approvalCommand(question)" :label="`${question.asker} 请求权限`">
    <template #head>
      <span>
        <span class="font-mono">{{ question.asker }}</span> 请求权限<span v-if="question.detail.tool_name" class="text-muted-foreground"> · {{ question.detail.tool_name }}</span>
      </span>
      <span v-if="variant !== 'mobile'" class="shrink-0 text-xs text-[#B4530B]">{{ approvalCardStatus(question) }}</span>
    </template>
    <p v-if="note" class="text-xs leading-5 text-[#B4530B]">{{ note }}</p>
    <p v-if="question.detail.description" class="text-xs leading-5 text-muted-foreground">{{ question.detail.description }}</p>
    <ReasonBox
      v-if="denying"
      placeholder="拒绝理由（可选，会发给员工）"
      confirm="确认拒绝"
      :busy="answer.isPending.value"
      @cancel="denying = false"
      @submit="(reason) => answer.mutate({ answer: 'deny', reason: reason || undefined })"
    />
    <CardActions v-else :variant="variant" :count="3">
      <CardButton kind="primary" :variant="variant" :busy="pending('allow')" @click="answer.mutate({ answer: 'allow' })">放行</CardButton>
      <CardButton kind="outline" :variant="variant" :busy="pending('allow_always')" @click="answer.mutate({ answer: 'allow_always' })">总是允许</CardButton>
      <CardButton kind="danger" :variant="variant" @click="denying = true">拒绝</CardButton>
    </CardActions>
  </CardFrame>
</template>
