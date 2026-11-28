<script setup lang="ts">
import { $t } from "@/i18n";
import { ChevronDown, ChevronUp } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { DeliverableView } from "@/api/views";
import { fileIcon, formatDateTime, libraryColumns, sizeOf, typeLabel, type SortKey } from "@/lib/library";
import { formatBytes } from "@/lib/bytes";
import { cn } from "@/lib/utils";
import { FILE_ICONS } from "../fileIcons";
import ItemLink from "../ItemLink.vue";
import { useWidth } from "./browser";
import ItemMenu from "./ItemMenu.vue";
import Owner from "./Owner.vue";

const props = defineProps<{ pid: string; items: DeliverableView[]; sort: { key: SortKey; dir: "asc" | "desc" } }>();
const emit = defineEmits<{ sort: [key: SortKey] }>();
const COLUMNS: { key: SortKey; label: string; className: string }[] = [
  { key: "name", label: $t("名称"), className: "min-w-0 flex-1" },
  { key: "owner", label: $t("所有者"), className: "w-32" },
  { key: "modified", label: $t("修改日期"), className: "w-40" },
  { key: "size", label: $t("大小"), className: "w-20" },
  { key: "type", label: $t("类型"), className: "w-16" },
];
const root = ref<HTMLElement | null>(null);
const width = useWidth(root);
const cols = computed(() => {
  const shown = libraryColumns(width.value || 1200);
  return COLUMNS.filter((c) => shown.has(c.key));
});
const ariaSort = (k: SortKey) => (props.sort.key === k ? (props.sort.dir === "asc" ? "ascending" : "descending") : "none");
</script>

<template>
  <div ref="root" role="table" :aria-label="$t('资料库')" class="text-[13px]">
    <div role="row" class="sticky top-0 z-[1] flex h-9 items-center gap-3 rounded-lg bg-raised px-3 text-xs text-foreground/80">
      <button
        v-for="c in cols"
        :key="c.key"
        type="button"
        role="columnheader"
        :aria-sort="ariaSort(c.key)"
        :class="cn('flex items-center gap-1 text-left font-medium hover:text-foreground', c.key === 'name' && 'pl-[30px]', c.className)"
        @click="emit('sort', c.key)"
      >
        {{ c.label }}
        <template v-if="sort.key === c.key">
          <ChevronUp v-if="sort.dir === 'asc'" class="size-3" />
          <ChevronDown v-else class="size-3" />
        </template>
      </button>
      <span class="w-7" />
    </div>
    <div v-for="d in items" :key="d.id" role="row" class="group flex h-[38px] items-center gap-3 rounded-lg px-3 hover:bg-raised/60">
      <span v-for="c in cols" :key="c.key" role="cell" :class="cn('truncate text-muted-foreground', c.className)">
        <ItemLink v-if="c.key === 'name'" :pid="pid" :item="d" class="flex min-w-0 items-center gap-3 text-foreground">
          <component :is="FILE_ICONS[fileIcon(d)]" aria-hidden="true" class="size-[18px] shrink-0 text-muted-foreground" :stroke-width="1.6" />
          <span class="truncate">{{ d.title }}</span>
          <span v-if="d.thread_no" class="shrink-0 text-xs text-muted-foreground">#{{ d.thread_no }}</span>
        </ItemLink>
        <Owner v-else-if="c.key === 'owner'" :by="d.published_by" />
        <span v-else-if="c.key === 'modified'" class="tabular-nums">{{ formatDateTime(d.updated_at) }}</span>
        <span v-else-if="c.key === 'size'" class="tabular-nums">{{ formatBytes(sizeOf(d), "decimal") }}</span>
        <template v-else>{{ typeLabel(d) }}</template>
      </span>
      <ItemMenu :item="d" :pid="pid" />
    </div>
  </div>
</template>
