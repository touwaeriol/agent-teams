<script setup lang="ts">
// One box of the flow chart: step name, who does it and the kind. Handles on the left (in) and
// right (out); drag from the right dot of one step to another to connect them.
import { Handle, Position } from "@vue-flow/core";
import { cn } from "@/lib/utils";

defineProps<{ data: { name: string; who: string; kind: string; tone: string; missing: boolean }; selected?: boolean }>();
</script>

<template>
  <div
    :class="
      cn(
        'min-w-[150px] max-w-[190px] rounded-xl border bg-panel px-3 py-2 text-left shadow-sm transition-colors',
        selected ? 'border-brand ring-2 ring-brand/30' : 'border-border',
        data.missing && 'border-destructive/60',
      )
    "
  >
    <Handle type="target" :position="Position.Left" class="!size-2.5 !border-2 !border-panel !bg-muted-foreground" />
    <div class="flex items-center justify-between gap-2">
      <span class="truncate text-[13px] font-semibold">{{ data.name }}</span>
      <span :class="cn('shrink-0 rounded px-1.5 text-[11px] leading-[18px]', data.tone)">{{ data.kind }}</span>
    </div>
    <div :class="cn('mt-0.5 truncate text-xs', data.missing ? 'text-destructive' : 'text-muted-foreground')">{{ data.who }}</div>
    <Handle type="source" :position="Position.Right" class="!size-2.5 !border-2 !border-panel !bg-brand" />
  </div>
</template>
