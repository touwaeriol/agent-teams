<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * The open thread's top bar in capsules (like the Claude app): a round ‹ back to the list; a
 * centred capsule with the status mark (blue spinner working, pulsing orange dot waiting, dashed
 * ring paused, ✓ done, ✕ failed), "#12 标题" (double-click renames in place) and the plan "2/7";
 * a right capsule with the node status and ✓ (archive) and ⋯ (the thread menu, with its
 * shortcuts); then expand and close where they apply.
 */
import { ChevronLeft, Maximize2, Minimize2, Pin, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { ThreadView } from "@/api/views";
import IconButton from "@/components/common/IconButton.vue";
import { threadLook } from "@/lib/present";
import { mainProgress, threadMark } from "@/lib/threadList";
import NodeStatus from "../NodeStatus.vue";
import ResolveButton from "../ResolveButton.vue";
import ThreadMark from "../ThreadMark.vue";
import ThreadMenu from "../ThreadMenu.vue";
import type { ThreadViewId } from "../threadMenu";
import type { ExpandLink, ThreadNav } from "./detail";
import TitleEdit from "./TitleEdit.vue";

const props = withDefaults(defineProps<{ thread: ThreadView; expand?: ExpandLink; fullHref?: string; nav?: ThreadNav; closable?: boolean }>(), {
  expand: undefined,
  fullHref: undefined,
  nav: undefined,
  closable: true,
});
const emit = defineEmits<{ back: []; close: []; view: [view: ThreadViewId] }>();
const look = computed(() => threadLook(props.thread));
const mark = computed(() => threadMark(props.thread));
const progress = computed(() => mainProgress(props.thread));
const renaming = ref(false);
const PILL = "border border-[#E7E5E0] bg-panel shadow-[0_1px_2px_rgba(0,0,0,0.05)] dark:border-border";
</script>

<template>
  <header class="flex h-14 shrink-0 items-center gap-2 border-b border-[#EFEDE8] px-2.5">
    <el-tooltip :content="$t('讨论串列表')" placement="bottom" :show-after="300" :hide-after="0" effect="dark" :show-arrow="false">
      <button
        type="button"
        :aria-label="$t('返回讨论串列表')"
        :class="[PILL, 'flex size-9 shrink-0 items-center justify-center rounded-full text-[#4A4843] hover:bg-raised dark:text-foreground/80']"
        @click="emit('back')"
      >
        <ChevronLeft class="size-[18px]" :stroke-width="1.9" />
      </button>
    </el-tooltip>
    <div class="flex min-w-0 flex-1 justify-center">
      <div
        :class="[PILL, 'flex h-9 min-w-0 max-w-full items-center gap-2 rounded-full pl-3 pr-3.5']"
        :title="$t('#{no} {title} · {label}{n}', { no: thread.no, title: thread.title, label: look.label, n: progress ? $t(' · 主任务 {label}', { label: progress.label }) : '' })"
        data-capsule="title"
      >
        <ThreadMark :thread="thread" :mark="mark" class="!mt-0" />
        <TitleEdit v-if="renaming" :thread="thread" @done="renaming = false" />
        <template v-else>
          <Pin v-if="thread.pinned_at" class="size-3 shrink-0 rotate-45 text-muted-foreground" :stroke-width="2" :aria-label="$t('已置顶')" />
          <span aria-current="page" class="truncate text-[13px] font-medium" @dblclick="renaming = true">#{{ thread.no }} {{ thread.title }}</span>
          <span v-if="progress" class="shrink-0 text-xs tabular-nums text-muted-foreground" :aria-label="$t('主任务完成 {label}', { label: progress.label })" data-progress="">{{ progress.label }}</span>
        </template>
      </div>
    </div>
    <div :class="[PILL, 'flex h-9 shrink-0 items-center gap-0.5 rounded-full pl-2.5 pr-0.5 [&_button]:rounded-full']" data-capsule="actions">
      <span class="mr-1 flex"><NodeStatus :thread="thread" /></span>
      <span class="max-sm:hidden"><ResolveButton :thread="thread" /></span>
      <ThreadMenu
        :thread="thread"
        :full-href="fullHref"
        :nav="nav"
        with-views
        shortcuts
        @view="(v) => emit('view', v)"
        @rename="renaming = true"
        @deleted="emit('back')"
      />
    </div>
    <IconButton v-if="expand" :label="expand.label" :to="expand.to" class="max-sm:hidden">
      <Minimize2 v-if="expand.collapse" :stroke-width="1.8" />
      <Maximize2 v-else :stroke-width="1.8" />
    </IconButton>
    <IconButton v-if="closable" :label="$t('关闭')" class="-ml-1" @click="emit('close')"><X :stroke-width="1.8" /></IconButton>
  </header>
</template>
