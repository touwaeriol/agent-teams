<script setup lang="ts">
/** One question of an open AskCard: lettered options (推荐, descriptions) and a free 其他 line. */
import { Check } from "lucide-vue-next";
import type { AskChoice, AskItem } from "@/api/views";
import { letter } from "@/lib/ask";
import { cn } from "@/lib/utils";
import LetterBadge from "./LetterBadge.vue";

defineProps<{ item: AskItem; choice: AskChoice; busy: boolean; who?: string }>();
const emit = defineEmits<{ pick: [label: string]; other: [text: string] }>();
</script>

<template>
  <fieldset class="flex min-w-0 flex-col gap-1.5" :disabled="busy">
    <legend class="mb-2 flex w-full items-start justify-between gap-3 text-[15px] leading-6 text-[#3D3B36]">
      <span>
        <span v-if="item.header" class="mr-2 rounded-md bg-[#F1EFEA] px-1.5 py-0.5 align-[2px] text-[11px] text-muted-foreground">{{ item.header }}</span>{{ item.question }}
      </span>
      <span v-if="who" class="shrink-0 pt-0.5 text-xs text-[#B4530B]">{{ who }} · 等你回答</span>
    </legend>
    <span v-if="item.multi_select" class="-mt-1 mb-1 text-xs text-muted-foreground">可多选</span>
    <button
      v-for="(o, i) in item.options"
      :key="o.label"
      type="button"
      :role="item.multi_select ? 'checkbox' : 'radio'"
      :aria-checked="choice.selected.includes(o.label)"
      :class="cn('flex w-full items-start gap-3 rounded-xl px-3 py-2.5 text-left transition-colors', choice.selected.includes(o.label) ? 'bg-[#F1EFEA]' : 'hover:bg-[#F7F6F3]')"
      @click="emit('pick', o.label)"
    >
      <LetterBadge>{{ letter(i) }}</LetterBadge>
      <span class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="flex flex-wrap items-center gap-2 text-sm font-medium">
          {{ o.label }}
          <span v-if="o.recommended" class="rounded-md bg-[#ECEAE4] px-1.5 py-px text-[11px] font-normal text-[#5E5B54]">推荐</span>
        </span>
        <span v-if="o.description" class="text-[13px] leading-5 text-muted-foreground">{{ o.description }}</span>
      </span>
      <Check v-if="choice.selected.includes(o.label)" aria-hidden="true" class="mt-1.5 size-4 shrink-0 text-[#5E5B54]" :stroke-width="2" />
    </button>
    <label :class="cn('flex items-center gap-3 rounded-xl px-3 py-2', choice.other ? 'bg-[#F1EFEA]' : '')">
      <LetterBadge>{{ letter(item.options.length) }}</LetterBadge>
      <input
        :value="choice.other ?? ''"
        placeholder="其他（自己写）"
        :aria-label="`其他回答：${item.question}`"
        class="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground/80"
        @input="emit('other', ($event.target as HTMLInputElement).value)"
      />
    </label>
  </fieldset>
</template>
