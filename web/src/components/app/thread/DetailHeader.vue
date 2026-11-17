<script setup lang="ts">
/**
 * The open thread's top bar as a left-aligned breadcrumb (like the Claude web app):
 * "讨论串 › ● #12 标题 2/7". "讨论串" goes back to the list; the status icon (ThreadStatusIcon)
 * shows working / queued / waiting / idle / paused / done / failed / archived; the title truncates
 * and double-click renames it in place. On the right, borderless icon buttons: node status,
 * ✓ (archive), ⋯ (the thread menu, with its shortcuts), then expand and close where they apply.
 */
import { ChevronRight, Maximize2, Minimize2, Pin, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { ThreadView } from "@/api/views";
import IconButton from "@/components/common/IconButton.vue";
import { threadLook } from "@/lib/present";
import { mainProgress, threadMark } from "@/lib/threadList";
import NodeStatus from "../NodeStatus.vue";
import ResolveButton from "../ResolveButton.vue";
import ThreadMenu from "../ThreadMenu.vue";
import ThreadStatusIcon from "../ThreadStatusIcon.vue";
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
const progress = computed(() => mainProgress(props.thread));
const mark = computed(() => threadMark(props.thread));
const renaming = ref(false);
</script>

<template>
  <header class="flex h-12 shrink-0 items-center gap-2 border-b border-[#EFEDE8] pl-4 pr-2 dark:border-border">
    <nav aria-label="面包屑" class="flex min-w-0 flex-1 items-center gap-1.5 text-[13px]" data-crumb="">
      <button
        type="button"
        aria-label="返回讨论串列表"
        title="返回讨论串列表"
        class="-ml-1.5 shrink-0 rounded-md px-1.5 py-0.5 text-muted-foreground transition-colors hover:bg-raised hover:text-foreground"
        @click="emit('back')"
      >
        讨论串
      </button>
      <ChevronRight class="size-3.5 shrink-0 text-muted-foreground/60" :stroke-width="1.5" aria-hidden="true" />
      <div
        class="flex min-w-0 flex-1 items-center gap-1.5"
        :title="`#${thread.no} ${thread.title} · ${look.label}${progress ? ` · 主任务 ${progress.label}` : ''}`"
        data-crumb="title"
      >
        <ThreadStatusIcon :mark="mark" :size="14" />
        <TitleEdit v-if="renaming" :thread="thread" @done="renaming = false" />
        <template v-else>
          <Pin v-if="thread.pinned_at" class="size-3 shrink-0 rotate-45 text-muted-foreground" :stroke-width="2" aria-label="已置顶" />
          <span aria-current="page" class="min-w-0 truncate font-normal text-foreground" @dblclick="renaming = true"
            ><span class="text-muted-foreground/70 tabular-nums">#{{ thread.no }}</span> {{ thread.title }}</span
          >
          <span v-if="progress" class="shrink-0 text-xs tabular-nums text-muted-foreground" :aria-label="`主任务完成 ${progress.label}`" data-progress="">{{ progress.label }}</span>
        </template>
      </div>
    </nav>
    <div class="flex shrink-0 items-center gap-0.5" data-actions="">
      <span class="flex size-8 items-center justify-center"><NodeStatus :thread="thread" /></span>
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
      <IconButton v-if="expand" :label="expand.label" :to="expand.to" class="max-sm:hidden">
        <Minimize2 v-if="expand.collapse" :stroke-width="1.8" />
        <Maximize2 v-else :stroke-width="1.8" />
      </IconButton>
      <IconButton v-if="closable" label="关闭" @click="emit('close')"><X :stroke-width="1.8" /></IconButton>
    </div>
  </header>
</template>
