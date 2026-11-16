<script setup lang="ts">
import { computed } from "vue";
import { fillDays, formatUSD } from "@/lib/projectSettings";

const props = defineProps<{ byDay: { day: string; cost_usd: number }[]; days: number }>();
const list = computed(() => fillDays(props.byDay, props.days));
const max = computed(() => Math.max(...list.value.map((d) => d.cost), 0.0001));
</script>

<template>
  <div class="rounded-xl border px-3.5 pb-2 pt-3">
    <span class="text-xs text-muted-foreground">每天费用</span>
    <div class="mt-2 flex h-24 items-end gap-1">
      <span
        v-for="d in list"
        :key="d.day"
        :title="`${d.day} · ${formatUSD(d.cost)}`"
        class="flex-1 rounded-t-sm bg-[#1F6FD1]/70"
        :style="{ height: `${Math.max(2, (d.cost / max) * 100)}%`, opacity: d.cost ? 1 : 0.25 }"
      />
    </div>
    <div class="mt-1 flex justify-between text-[11px] text-muted-foreground">
      <span>{{ list[0]?.day.slice(5) }}</span>
      <span>{{ list[list.length - 1]?.day.slice(5) }}</span>
    </div>
  </div>
</template>
