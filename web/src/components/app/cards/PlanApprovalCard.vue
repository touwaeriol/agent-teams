<script setup lang="ts">
import { $t } from "@/i18n";
/** A plan an ask-role employee wants approved (ExitPlanMode): the plan as Markdown, 批准计划 / 提修改意见. */
import { computed, ref } from "vue";
import type { QuestionView } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import CardActions from "./CardActions.vue";
import CardButton from "./CardButton.vue";
import CardFrame from "./CardFrame.vue";
import ReasonBox from "./ReasonBox.vue";
import { useAnswer } from "./useAnswer";
import type { CardVariant } from "./variant";

const props = withDefaults(defineProps<{ cid: string; pid: string; question: QuestionView; variant?: CardVariant }>(), { variant: "soft" });
const answer = useAnswer(() => props.cid, () => props.question, () => props.pid);
const rejecting = ref(false);
const plan = computed(() => (typeof props.question.detail.input?.plan === "string" ? props.question.detail.input.plan : ""));
</script>

<template>
  <CardFrame :variant="variant" :label="$t('{asker} 的计划', { asker: question.asker })">
    <template #head>
      <span><span class="font-mono">{{ question.asker }}</span> {{ $t("的计划等你批准 ·") }} <span class="text-[#B4530B]">{{ $t("等你处理") }}</span></span>
    </template>
    <Markdown v-if="plan" :source="plan" class="rounded-lg border border-[#F3E2D3] bg-white px-3 py-2 text-[13px] leading-6" />
    <p v-else class="text-xs text-muted-foreground">{{ $t("计划内容见上面的计划卡片。") }}</p>
    <ReasonBox
      v-if="rejecting"
      :placeholder="$t('哪里要改？会发给员工')"
      :confirm="$t('发回修改')"
      :busy="answer.isPending.value"
      @cancel="rejecting = false"
      @submit="(reason) => answer.mutate({ answer: 'deny', reason: reason || undefined })"
    />
    <CardActions v-else :variant="variant" :count="2">
      <CardButton kind="primary" :variant="variant" :busy="answer.isPending.value" @click="answer.mutate({ answer: 'allow' })">{{ $t("批准计划") }}</CardButton>
      <CardButton kind="outline" :variant="variant" @click="rejecting = true">{{ $t("提修改意见") }}</CardButton>
    </CardActions>
  </CardFrame>
</template>
