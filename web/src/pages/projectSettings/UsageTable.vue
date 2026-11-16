<script setup lang="ts">
import { formatTokens, formatUSD } from "@/lib/projectSettings";
import { inputTokens, type UsageRow } from "./logic";

defineProps<{ title: string; rows: UsageRow[] }>();
</script>

<template>
  <div v-if="rows.length > 0" class="flex flex-col">
    <span class="pb-1 text-[13px] font-medium">{{ title }}</span>
    <div class="flex h-8 items-center gap-3 rounded-lg bg-raised/70 px-3 text-xs text-muted-foreground">
      <span class="min-w-0 flex-1">名称</span>
      <span class="w-20 text-right">输入</span>
      <span class="w-20 text-right">输出</span>
      <span class="w-16 text-right">费用</span>
    </div>
    <div v-for="r in rows" :key="r.key" class="flex h-9 items-center gap-3 border-b border-border/60 px-3 text-[13px] last:border-0">
      <span class="min-w-0 flex-1 truncate">{{ r.name }}</span>
      <span class="w-20 text-right tabular-nums text-muted-foreground">{{ formatTokens(inputTokens(r.tokens)) }}</span>
      <span class="w-20 text-right tabular-nums text-muted-foreground">{{ formatTokens(r.tokens.output) }}</span>
      <span class="w-16 text-right tabular-nums">{{ formatUSD(r.cost) }}</span>
    </div>
  </div>
</template>
