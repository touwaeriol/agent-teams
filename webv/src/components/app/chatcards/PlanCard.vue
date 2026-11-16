<script setup lang="ts">
/** An ExitPlanMode plan, as Markdown; long plans fold. */
import { NotebookPen } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { MessageView } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import { cn } from "@/lib/utils";
import { CARD } from "./style";

const props = defineProps<{ message: MessageView }>();
const long = computed(() => props.message.content.length > 900);
const open = ref(!long.value);
</script>

<template>
  <section aria-label="计划" :class="cn(CARD, 'flex flex-col gap-2 px-4 py-3')">
    <span class="flex items-center gap-2 text-[13px] text-muted-foreground">
      <NotebookPen aria-hidden="true" class="size-4" :stroke-width="1.8" />
      {{ message.meta.employee ?? message.author }} 的计划
    </span>
    <div :class="cn('relative', !open && 'max-h-48 overflow-hidden')">
      <Markdown :source="message.content" class="text-sm leading-6 [&_h1]:text-base [&_h2]:text-[15px] [&_h3]:text-sm" />
      <div v-if="!open" class="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-panel" />
    </div>
    <button v-if="long" type="button" class="self-start text-xs text-muted-foreground hover:text-foreground" @click="open = !open">{{ open ? "收起" : "展开全部" }}</button>
  </section>
</template>
