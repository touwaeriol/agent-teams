<script setup lang="ts">
import { $t } from "@/i18n";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { OverviewView } from "@/api/views";
import { reviewText, waitingFrom, waitingText } from "./logic";

const props = defineProps<{ pid: string; overview: OverviewView }>();
const questions = computed(() => props.overview.waiting.questions);
const reviews = computed(() => props.overview.waiting.reviews);
const total = computed(() => questions.value.length + reviews.value.length);
</script>

<template>
  <section class="flex flex-col gap-2.5 rounded-xl border border-[#F0C9A8] bg-panel p-4">
    <h2 class="text-sm font-semibold">{{ $t("等你处理 · {total}", { total }) }}</h2>
    <span v-if="total === 0" class="text-[13px] text-muted-foreground">{{ $t("现在没有要你处理的事。") }}</span>
    <RouterLink
      v-for="q in questions"
      :key="q.id"
      :to="q.thread_no ? `/projects/${pid}/threads/${q.thread_no}` : `/projects/${pid}`"
      class="flex flex-col gap-0.5 rounded-lg bg-[#FFF9F4] px-2.5 py-2"
    >
      <span class="text-[13px]">{{ waitingText(q) }}</span>
      <span class="text-xs text-muted-foreground">{{ waitingFrom(q) }}</span>
    </RouterLink>
    <RouterLink v-for="t in reviews" :key="t.id" :to="`/projects/${pid}/threads/${t.no}`" class="flex flex-col gap-0.5 rounded-lg bg-[#FFF9F4] px-2.5 py-2">
      <span class="text-[13px]">{{ reviewText(t) }}</span>
      <span class="text-xs text-muted-foreground">{{ $t("#{no} · 待验收", { no: t.no }) }}</span>
    </RouterLink>
  </section>
</template>
