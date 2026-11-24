<script setup lang="ts">
/**
 * The permission menu (title 模式): one row per mode the server lists, with its description, a
 * check on the current one and its number (the shortcut while the menu is open; not on touch).
 * 跳过权限确认 sits under a divider. While a switch waits for the node every row is disabled and
 * the target spins. The footer says when a switch takes effect.
 */
import { Check, Loader2 } from "lucide-vue-next";
import { computed } from "vue";
import type { ModeRow } from "@/lib/sessionControls";
import ModeIcon from "./ModeIcon.vue";

const props = defineProps<{ rows: ModeRow[]; current: string; pending?: string; runtimeLabel: string; notes: string[]; touch?: boolean; disabled?: boolean }>();
const emit = defineEmits<{ pick: [id: string] }>();
const normal = computed(() => props.rows.filter((r) => !r.danger));
const danger = computed(() => props.rows.filter((r) => r.danger));
/** Rows are disabled while any switch waits (a mode pick, or a model or effort pick: disabled). */
const busy = computed(() => Boolean(props.pending) || Boolean(props.disabled));
</script>

<template>
  <div class="flex flex-col text-[13px]" role="menu" aria-label="权限模式" data-testid="mode-menu">
    <div class="flex items-center justify-between px-2.5 pb-1 pt-0.5 text-xs text-muted-foreground">
      <span class="font-semibold text-foreground">模式 · {{ runtimeLabel }}</span>
    </div>
    <template v-for="(group, gi) in [normal, danger]" :key="gi">
      <div v-if="gi === 1 && group.length" class="mx-2.5 my-1 border-t" />
      <button
        v-for="r in group"
        :key="r.id"
        type="button"
        role="menuitemradio"
        :aria-checked="r.id === current && !busy"
        :disabled="busy"
        :data-mode="r.id"
        :class="['flex w-full items-start gap-2.5 rounded-lg px-2.5 py-[7px] text-left', touch ? 'py-2.5' : '', busy ? 'cursor-default opacity-70' : 'hover:bg-accent']"
        @click="emit('pick', r.id)"
      >
        <ModeIcon :mode="r.id" :class="['mt-0.5 size-3.5 shrink-0', r.danger ? 'text-[#C2570C]' : 'text-muted-foreground']" />
        <span class="flex min-w-0 flex-1 flex-col leading-5">
          <span class="font-medium">{{ r.label }}</span>
          <span class="text-xs text-muted-foreground">{{ r.hint }}</span>
        </span>
        <Loader2 v-if="pending === r.id" class="mt-0.5 size-3.5 shrink-0 animate-spin text-[#2F6FDB]" />
        <Check v-else-if="r.id === current && !pending" class="mt-0.5 size-3.5 shrink-0" data-testid="mode-current" />
        <span v-if="!touch" class="mt-0.5 shrink-0 rounded-[5px] border bg-background px-[5px] font-mono text-[11px] leading-4 text-muted-foreground">{{ r.key }}</span>
      </button>
    </template>
    <p v-if="!rows.length" class="px-2.5 py-1.5 text-xs text-muted-foreground">这个会话现在不能切换权限模式。</p>
    <p v-for="n in notes" :key="n" class="px-2.5 pt-1 text-[11.5px] leading-[18px] text-muted-foreground">{{ n }}</p>
    <p v-if="pending" class="px-2.5 pt-1 text-[11.5px] leading-[18px] text-[#2F6FDB]">已提交，等节点确认后才算切换成功。</p>
  </div>
</template>
