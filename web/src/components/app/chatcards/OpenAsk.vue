<script setup lang="ts">
/** An open question: pick (A/B/C…) or write 其他, then submit; single one-click questions answer on pick. */
import { computed, ref } from "vue";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { AskChoice, AskItem, QuestionView } from "@/api/views";
import Spinner from "@/components/common/Spinner.vue";
import { answerBody, complete, emptyChoices, oneClick, toggle, typeOther } from "@/lib/ask";
import { cn } from "@/lib/utils";
import AskBlock from "./AskBlock.vue";
import { CARD } from "./style";

const props = defineProps<{ cid: string; pid: string; question: QuestionView; items: AskItem[]; who?: string }>();
const choices = ref<AskChoice[]>(emptyChoices(props.items));
const answer = useAction(
  (c: AskChoice[]) => {
    const body = answerBody(props.question, c);
    return conversations.answer(props.cid, props.question.id, body.answer, undefined, body.answers);
  },
  { invalidate: () => [keys.questions(props.cid), keys.waiting(props.pid), keys.threads(props.pid)] },
);
const quick = computed(() => oneClick(props.items));
const set = (i: number, c: AskChoice) => (choices.value = choices.value.map((x, j) => (j === i ? c : x)));
function pick(i: number, label: string) {
  const next = toggle(choices.value[i], label, Boolean(props.items[i].multi_select));
  if (quick.value && next.selected.length) return answer.mutate([next]);
  set(i, next);
}
const showSubmit = computed(() => !quick.value || Boolean(choices.value[0]?.other));
</script>

<template>
  <section :aria-label="`${who ?? question.asker} 的提问`" :class="cn(CARD, 'flex flex-col gap-4 px-4 py-3.5')">
    <AskBlock
      v-for="(item, i) in items"
      :key="i"
      :item="item"
      :choice="choices[i]"
      :busy="answer.isPending.value"
      :who="i === 0 ? who : undefined"
      @pick="(l) => pick(i, l)"
      @other="(t) => set(i, typeOther(choices[i], t, Boolean(item.multi_select)))"
    />
    <div v-if="showSubmit || answer.isPending.value" class="flex items-center justify-end gap-2">
      <Spinner v-if="answer.isPending.value" class="size-3.5" />
      <button
        v-if="showSubmit"
        type="button"
        :disabled="!complete(choices) || answer.isPending.value"
        class="h-8 rounded-lg bg-primary px-3.5 text-[13px] text-primary-foreground disabled:opacity-40"
        @click="answer.mutate(choices)"
      >
        提交
      </button>
    </div>
  </section>
</template>
