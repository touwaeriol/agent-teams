<script setup lang="ts" generic="T">
// A horizontally scrollable table with a header row. Each column renders the scoped slot
// `cell-<key>` (`{ row }`), falling back to `row[key]`.
import { cn } from "@/lib/utils";

export interface Column {
  key: string;
  header: string;
  class?: string;
}

withDefaults(defineProps<{ columns: Column[]; rows: T[]; rowKey: (row: T) => string; empty?: string; minWidth?: number; class?: string }>(), {
  minWidth: 640,
  empty: undefined,
  class: undefined,
});
const cell = (row: T, key: string) => String((row as Record<string, unknown>)[key] ?? "");
</script>

<template>
  <div :class="cn('overflow-x-auto rounded-lg border', $props.class)">
    <table class="w-full border-collapse text-[13px]" :style="{ minWidth: `${minWidth}px` }">
      <thead class="bg-well text-left text-muted-foreground">
        <tr>
          <th v-for="c in columns" :key="c.key" :class="cn('px-3 py-2.5 font-medium', c.class)">{{ c.header }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="rowKey(row)" class="border-t">
          <td v-for="c in columns" :key="c.key" :class="cn('px-3 py-2.5 align-middle', c.class)">
            <slot :name="`cell-${c.key}`" :row="row">{{ cell(row, c.key) }}</slot>
          </td>
        </tr>
        <tr v-if="rows.length === 0 && (empty || $slots.empty)">
          <td :colspan="columns.length" class="px-3 py-6 text-center text-muted-foreground"><slot name="empty">{{ empty }}</slot></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
