<script setup lang="ts">
import { $t } from "@/i18n";
/** The "/" and "@" popup above the message box: grouped rows, arrow keys move, Enter or Tab picks, Esc closes. */
import { computed, nextTick, ref, watch } from "vue";
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import type { AssistItem } from "./assist";
import type { ComposerAssist } from "./useComposerAssist";

const props = defineProps<{ assist: ComposerAssist }>();
const list = ref<HTMLElement | null>(null);
const rows = computed(() => props.assist.items.value.map((item, i) => ({ item, i, head: i === 0 || props.assist.items.value[i - 1]!.group !== item.group })));
const slash = computed(() => props.assist.trigger.value?.kind === "slash");
const empty = computed(() => (slash.value ? $t("没有匹配的命令或技能") : $t("没有匹配的文件、讨论串、交付物或记忆")));
watch(
  () => props.assist.active.value,
  async (i) => {
    await nextTick();
    list.value?.querySelector(`[data-index="${i}"]`)?.scrollIntoView({ block: "nearest" });
  },
);
const pick = (item: AssistItem) => void props.assist.choose(item);
</script>

<template>
  <div
    v-if="assist.open.value"
    class="absolute inset-x-0 bottom-full z-30 mb-2 overflow-hidden rounded-xl border bg-panel shadow-[0_8px_28px_-8px_rgba(0,0,0,0.18)]"
    data-testid="assist-popup"
  >
    <ul ref="list" role="listbox" :aria-label="slash ? $t('斜杠命令') : $t('引用')" class="max-h-[280px] overflow-y-auto py-1.5">
      <template v-for="r in rows" :key="r.item.key">
        <li v-if="r.head" role="presentation" class="px-3 pb-1 pt-2 text-[11px] font-medium text-muted-foreground">{{ r.item.group }}</li>
        <li
          role="option"
          :data-index="r.i"
          :aria-selected="r.i === assist.active.value"
          :class="cn('mx-1.5 flex cursor-pointer items-baseline gap-3 rounded-lg px-2.5 py-1.5 text-[13px]', r.i === assist.active.value && 'bg-raised')"
          @mousedown.prevent="pick(r.item)"
          @mousemove="assist.highlight(r.i)"
        >
          <span :class="cn('shrink-0', slash || r.item.group === $t('文件') ? 'font-mono text-[12.5px]' : 'font-medium')">{{ r.item.label }}</span>
          <span class="min-w-0 truncate text-xs text-muted-foreground">{{ r.item.detail }}</span>
        </li>
      </template>
      <li v-if="assist.loading.value" class="flex items-center gap-2 px-3 py-2 text-xs text-muted-foreground"><Spinner class="size-3.5" />{{ $t("加载中") }}</li>
      <li v-else-if="rows.length === 0" class="px-3 py-2 text-xs text-muted-foreground">{{ empty }}</li>
    </ul>
    <p v-if="assist.note.value" class="border-t px-3 py-1.5 text-[11px] text-muted-foreground">{{ assist.note.value }}</p>
    <p class="border-t px-3 py-1.5 text-[11px] text-muted-foreground">{{ $t("↑↓ 选择 · Enter 确定 · Esc 关闭") }}</p>
  </div>
</template>
