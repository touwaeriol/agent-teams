<script setup lang="ts">
import { computed } from "vue";
import type { OverviewView } from "@/api/views";
import { cn } from "@/lib/utils";
import { FILTERS, filterThreads, type Filter } from "./logic";
import ThreadCard from "./ThreadCard.vue";

const props = defineProps<{ overview: OverviewView }>();
const filter = defineModel<Filter>("filter", { required: true });
const shown = computed(() => filterThreads(props.overview.threads, filter.value));
const count = (f: Filter) => filterThreads(props.overview.threads, f).length;
const chip = (on: boolean) => cn("h-[30px] rounded-full border px-2.5 text-xs", on ? "border-foreground bg-foreground/5" : "border-input bg-white");
</script>

<template>
  <section class="flex min-w-0 flex-[999_1_520px] flex-col gap-3">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <h2 class="text-[15px] font-semibold">讨论串</h2>
      <div role="group" aria-label="按状态筛选" class="flex flex-wrap gap-1">
        <button v-for="[id, label] in FILTERS" :key="id" type="button" :aria-pressed="filter === id" :class="chip(filter === id)" @click="filter = id">{{ label }} {{ count(id) }}</button>
      </div>
    </div>
    <p v-if="shown.length === 0" class="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">这里还没有讨论串。</p>
    <ThreadCard v-for="t in shown" :key="t.id" :pid="overview.project.id" :thread="t" />
  </section>
</template>
