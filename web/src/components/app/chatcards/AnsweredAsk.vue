<script setup lang="ts">
/** An answered (or cancelled) question: each question with the chosen rows ticked. */
import { computed } from "vue";
import type { AskItem, QuestionView } from "@/api/views";
import { answeredInChat, answeredRows, letter } from "@/lib/ask";
import { cn } from "@/lib/utils";
import ChosenRow from "./ChosenRow.vue";
import { CARD } from "./style";

const props = defineProps<{ question: QuestionView; items: AskItem[]; who?: string }>();
const rows = computed(() => answeredRows(props.question));
const inChat = computed(() => answeredInChat(props.question));
const rowOf = (i: number, item: AskItem) => rows.value[i] ?? { question: item.question, selected: [] as string[], other: undefined };
const optionIndex = (item: AskItem, label: string) => item.options.findIndex((o) => o.label === label);
</script>

<template>
  <section :aria-label="`${who ?? question.asker} 的提问（${inChat ? '已关闭' : '已回答'}）`" :class="cn(CARD, 'flex flex-col gap-3 px-4 py-3.5')">
    <div v-for="(item, i) in items" :key="i" class="flex flex-col gap-1.5">
      <p :class="cn('text-[15px] leading-6', inChat ? 'text-muted-foreground' : 'text-[#3D3B36]')">
        <span v-if="item.header && items.length > 1" class="mr-2 rounded-md bg-[#F1EFEA] px-1.5 py-0.5 align-[2px] text-[11px] text-muted-foreground">{{ item.header }}</span>{{ item.question }}
      </p>
      <ChosenRow
        v-for="label in rowOf(i, item).selected"
        :key="label"
        :badge="letter(Math.max(optionIndex(item, label), 0))"
        :label="label"
        :recommended="item.options[optionIndex(item, label)]?.recommended"
        :description="item.options[optionIndex(item, label)]?.description"
      />
      <ChosenRow v-if="rowOf(i, item).other" :badge="letter(item.options.length)" :label="rowOf(i, item).other!" />
      <p v-if="question.status === 'cancelled'" class="text-xs text-muted-foreground">问题已取消</p>
    </div>
    <p v-if="inChat" class="text-xs text-muted-foreground">已关闭 · 已在对话里回答<template v-if="question.answer">：{{ question.answer }}</template></p>
  </section>
</template>
