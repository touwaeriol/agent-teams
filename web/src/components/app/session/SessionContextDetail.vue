<script setup lang="ts">
/**
 * The breakdown under the 上下文 bar of a session (Claude only, design #53 预设 3): a coloured bar
 * of what fills the window, the categories with tokens and share, then below the divider the
 * autocompact reserve, free space and the deferred tools (不计入: outside the window). An MCP row
 * expands into its tools grouped by server; long names are cut and shown whole on hover.
 */
import { ChevronRight } from "lucide-vue-next";
import { computed, ref } from "vue";
import { cn } from "@/lib/utils";
import { categoryRows, HATCH, mcpGroups, segments, swatchStyle, type CategoryRow, type ContextDetail } from "./contextDetail";

const props = defineProps<{ detail: ContextDetail }>();
const rows = computed(() => categoryRows(props.detail));
const segs = computed(() => segments(props.detail));
const open = ref<Record<string, boolean>>({});
const groupsOf = (r: CategoryRow) => mcpGroups(r.tools, props.detail.max_tokens);
function toggle(r: CategoryRow) {
  open.value = { ...open.value, [r.key]: !open.value[r.key] };
}
const swatch = (r: CategoryRow) => swatchStyle(r.kind, r.color);
</script>

<template>
  <div class="flex flex-col gap-2" data-testid="context-detail">
    <div class="flex h-1.5 overflow-hidden rounded-sm bg-raised" data-testid="context-segments">
      <span
        v-for="s in segs"
        :key="s.key"
        class="h-full shrink-0"
        :style="{ width: s.width, background: s.buffer ? HATCH : s.color }"
        :title="s.label"
      />
    </div>
    <div class="mt-2 text-xs text-muted-foreground">按类别</div>
    <ul class="flex flex-col gap-px text-[13px]" data-testid="context-categories">
      <template v-for="r in [...rows.used, null, ...rows.rest]" :key="r?.key ?? 'divider'">
        <li v-if="!r" class="my-1.5 h-px bg-border" data-testid="context-divider" />
        <li v-else>
          <component
            :is="r.tools.length ? 'button' : 'div'"
            :type="r.tools.length ? 'button' : undefined"
            :aria-expanded="r.tools.length ? Boolean(open[r.key]) : undefined"
            :class="cn('flex w-full items-center gap-2 rounded-md px-1 py-1 text-left', r.tools.length && 'cursor-pointer hover:bg-raised', r.kind === 'deferred' && 'text-muted-foreground')"
            :data-testid="`context-row-${r.key}`"
            @click="r.tools.length && toggle(r)"
          >
            <span class="size-[9px] shrink-0 rounded-[2px]" :style="swatch(r)" />
            <span class="flex min-w-0 flex-1 items-center gap-1">
              <span class="truncate">{{ r.label }}</span>
              <template v-if="r.tools.length">
                <ChevronRight :class="cn('size-3 shrink-0 text-muted-foreground transition-transform', open[r.key] && 'rotate-90')" />
                <span class="shrink-0 text-[11.5px] text-muted-foreground">{{ r.tools.length }} 个</span>
              </template>
            </span>
            <span class="min-w-12 text-right tabular-nums">{{ r.tokensText }}</span>
            <span class="w-12 text-right tabular-nums text-muted-foreground">{{ r.pctText }}</span>
          </component>
          <ul v-if="r.tools.length && open[r.key]" class="flex flex-col gap-px pb-1" :data-testid="`context-tools-${r.key}`">
            <template v-for="g in groupsOf(r)" :key="g.server">
              <li class="truncate pl-[22px] pt-1.5 text-[11.5px] text-muted-foreground" :title="g.server">{{ g.server }}</li>
              <li v-for="t in g.tools" :key="t.name" class="flex items-center gap-2 rounded-md px-1 py-0.5 pl-[22px] text-[12.5px]" :title="t.name">
                <span class="min-w-0 flex-1 truncate font-mono text-xs">{{ t.short }}</span>
                <span class="min-w-12 text-right tabular-nums">{{ t.tokensText }}</span>
                <span class="w-12 text-right tabular-nums text-muted-foreground">{{ t.pctText }}</span>
              </li>
            </template>
          </ul>
        </li>
      </template>
    </ul>
  </div>
</template>
