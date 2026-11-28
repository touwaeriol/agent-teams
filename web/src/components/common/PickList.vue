<script setup lang="ts" generic="T">
// A scrollable list where one item can be picked. The default slot renders an item: `{ item, picked }`.
import { $t } from "@/i18n";
import { cn } from "@/lib/utils";

defineProps<{ items: T[]; itemKey: (item: T) => string; selected?: string; empty?: string; loading?: boolean; error?: string; class?: string }>();
const emit = defineEmits<{ pick: [item: T] }>();
</script>

<template>
  <div :class="cn('scroll-thin max-h-56 overflow-y-auto rounded-lg border bg-well', $props.class)">
    <div v-if="loading" class="p-3 text-xs text-muted-foreground">{{ $t("加载中……") }}</div>
    <div v-if="error" class="p-3 text-xs text-destructive">{{ error }}</div>
    <div v-if="!loading && !error && items.length === 0 && empty" class="p-3 text-xs text-muted-foreground">{{ empty }}</div>
    <button
      v-for="item in items"
      :key="itemKey(item)"
      type="button"
      :class="
        cn(
          'flex w-full items-center gap-2 border-b border-border/60 px-3 py-2 text-left last:border-0 hover:bg-raised',
          itemKey(item) === selected && 'bg-accent text-accent-foreground hover:bg-accent',
        )
      "
      @click="emit('pick', item)"
    >
      <slot :item="item" :picked="itemKey(item) === selected" />
    </button>
  </div>
</template>
