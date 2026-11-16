<script setup lang="ts">
// 使用情况: cost and tokens over 7 or 30 days, per day, per thread and per employee.
import { computed, ref } from "vue";
import { useProjectUsage } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { formatTokens, formatUSD } from "@/lib/projectSettings";
import { cn } from "@/lib/utils";
import DayBars from "./DayBars.vue";
import Heading from "./Heading.vue";
import { employeeRows, inputTokens, threadRows } from "./logic";
import UsageTable from "./UsageTable.vue";

const props = defineProps<{ pid: string }>();
const days = ref(7);
const usage = useProjectUsage(() => props.pid, days);
const u = computed(() => usage.data.value);
const stats = computed(() => {
  const x = u.value;
  if (!x) return [];
  return [
    { label: "费用（估算）", value: formatUSD(x.cost_usd_est) },
    { label: "输入 tokens", value: formatTokens(inputTokens(x.tokens)) },
    { label: "输出 tokens", value: formatTokens(x.tokens.output) },
    { label: "回合", value: String(x.turns) },
  ];
});
</script>

<template>
  <div class="flex flex-col gap-3">
    <Heading>
      使用情况
      <template #action>
        <span role="group" aria-label="时间范围" class="flex gap-0.5 rounded-lg bg-raised p-0.5 text-[13px]">
          <button v-for="d in [7, 30]" :key="d" type="button" :aria-pressed="days === d" :class="cn('h-7 rounded-md px-2.5', days === d ? 'bg-panel shadow-sm' : 'text-muted-foreground')" @click="days = d">
            最近 {{ d }} 天
          </button>
        </span>
      </template>
    </Heading>
    <Loading v-if="usage.isLoading.value" />
    <p v-if="u && u.turns === 0" class="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">最近 {{ days }} 天没有用量记录。</p>
    <template v-if="u && u.turns > 0">
      <div class="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
        <div v-for="s in stats" :key="s.label" class="flex flex-col gap-1 rounded-xl border px-3.5 py-3">
          <span class="text-xs text-muted-foreground">{{ s.label }}</span>
          <span class="text-lg font-semibold tabular-nums">{{ s.value }}</span>
        </div>
      </div>
      <DayBars :by-day="u.by_day ?? []" :days="days" />
      <UsageTable title="按讨论串" :rows="threadRows(u)" />
      <UsageTable title="按员工" :rows="employeeRows(u)" />
      <p class="text-xs text-muted-foreground">费用来自 Claude 每个回合结束时报告的数字，订阅账号下是按 API 价格的估算。输入含缓存读写。</p>
    </template>
  </div>
</template>
