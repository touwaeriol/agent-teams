<script setup lang="ts">
// One row of the panel list. Rich (等你处理 / 进行中): mark, "#N 标题" (PR icon when it opened
// one), the node icon and last activity on the right (⋯ menu on hover), a grey one-line
// progress, then small tags: plan "2/7" and "N 个交付物" (opens the thread's deliverables), and
// the step being worked on (one line). The "2/7" tag is a button that unfolds the whole plan
// as a checklist card under the row.
// Compact (已暂停 / 已完成 / 已归档): mark, title and time on one short line.
// A pin marks a pinned thread; "提醒" a reminder that fired, "提醒 明天 09:00" one still to come.
// A subthread under its parent is indented; one whose parent is elsewhere says "#n 的子讨论串";
// a parent with subthreads right under it gets a "n 个子讨论串" toggle that folds them.
import { Bell, ChevronDown, CornerDownRight, GitPullRequest, ListChecks, Package, Pin } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import type { ThreadView } from "@/api/views";
import { cn } from "@/lib/utils";
import { currentStep, deliverableCount, lastActivity, mainProgress, reminderLabel, shortAgo, type RowMark, type RowNest } from "@/lib/threadList";
import PlanChecklist from "./plan/PlanChecklist.vue";
import NodeStatus from "./NodeStatus.vue";
import TitleEdit from "./thread/TitleEdit.vue";
import ThreadMark from "./ThreadMark.vue";
import ThreadMenu from "./ThreadMenu.vue";

const props = defineProps<{ thread: ThreadView; line: string; menu: boolean; mark: RowMark; now: number; compact?: boolean; nest?: RowNest; subsFolded?: boolean }>();
const emit = defineEmits<{ open: [no: number]; toggleSubs: [] }>();
const at = computed(() => lastActivity(props.thread));
const ago = computed(() => shortAgo(at.value, props.now));
const renaming = ref(false);
const reminder = computed(() => (props.thread.remind_at ? `提醒 ${reminderLabel(props.thread.remind_at, props.now)}` : ""));
const progress = computed(() => mainProgress(props.thread));
const delivered = computed(() => deliverableCount(props.thread));
const step = computed(() => currentStep(props.thread.main_tasks));
const planOpen = ref(false);
const TAG = "inline-flex h-5 items-center gap-1 rounded-md border bg-panel px-1.5 text-[11px] leading-none text-muted-foreground";
</script>

<template>
  <div
    :class="cn('group -mx-2 rounded-lg hover:bg-background', compact && 'rounded-md', nest?.nested && 'ml-3')"
    data-row=""
    :data-compact="compact ? '' : undefined"
    :data-subthread="thread.parent_no ?? undefined"
  >
    <div :class="cn('flex', compact ? 'items-center' : 'items-start')">
      <CornerDownRight v-if="nest?.nested" aria-hidden="true" :class="cn('ml-1 size-3.5 shrink-0 text-[#B7B3AA]', compact ? '' : 'mt-2.5')" :stroke-width="1.8" />
      <div v-if="renaming" :class="cn('flex min-w-0 flex-1 items-center gap-2.5 pl-2 pr-1', compact ? 'py-0.5' : 'py-1')">
        <ThreadMark :thread="thread" :mark="mark" />
        <TitleEdit :thread="thread" @done="renaming = false" />
      </div>
      <button v-else-if="compact" type="button" class="flex h-8 min-w-0 flex-1 items-center gap-2.5 pl-2 pr-1 text-left" :title="line || undefined" @click="emit('open', thread.no)">
        <ThreadMark :thread="thread" :mark="mark" class="!mt-0" />
        <Pin v-if="thread.pinned_at" class="size-3 shrink-0 rotate-45 text-muted-foreground" :stroke-width="2" aria-label="已置顶" />
        <span :class="cn('truncate text-[13px]', mark === 'archived' || mark === 'done' ? 'text-foreground/70' : 'text-foreground/85')">#{{ thread.no }} {{ thread.title }}</span>
        <span v-if="nest?.parentElsewhere" class="shrink-0 text-[11px] text-muted-foreground" data-parent-tag="">#{{ nest.parentElsewhere }} 的子讨论串</span>
        <Bell v-if="thread.remind_at" class="size-3 shrink-0 text-muted-foreground" :stroke-width="1.8" :aria-label="reminder" data-reminder="" />
      </button>
      <button v-else type="button" class="flex min-w-0 flex-1 gap-2.5 py-2 pl-2 pr-1 text-left" @click="emit('open', thread.no)">
        <ThreadMark :thread="thread" :mark="mark" />
        <span class="flex min-w-0 flex-1 flex-col gap-[3px]">
          <span class="flex min-w-0 items-center gap-1.5 text-sm font-medium">
            <Pin v-if="thread.pinned_at" class="size-3 shrink-0 rotate-45 text-muted-foreground" :stroke-width="2" aria-label="已置顶" />
            <span class="truncate">#{{ thread.no }} {{ thread.title }}</span>
            <span v-if="nest?.parentElsewhere" class="shrink-0 text-[11px] font-normal text-muted-foreground" data-parent-tag="">#{{ nest.parentElsewhere }} 的子讨论串</span>
            <GitPullRequest v-if="thread.pr_number" class="size-3.5 shrink-0 text-muted-foreground" :stroke-width="1.8" aria-label="有 PR" />
            <span v-if="thread.reminded_at" class="inline-flex shrink-0 items-center gap-0.5 rounded bg-[#FBEFE6] px-1 text-[11px] font-medium leading-4 text-[#B4541A] dark:bg-[#3a2a1f] dark:text-[#E8A06F]">
              <Bell class="size-2.5" :stroke-width="2.2" />提醒
            </span>
          </span>
          <span v-if="line" class="truncate text-xs leading-[18px] text-muted-foreground">{{ line }}</span>
          <span v-if="reminder" class="flex items-center gap-1 text-xs leading-[18px] text-muted-foreground" data-reminder="">
            <Bell class="size-3 shrink-0" :stroke-width="1.8" />{{ reminder }}
          </span>
        </span>
      </button>
      <span :class="cn('relative flex shrink-0 items-center justify-end gap-2 pr-2', compact ? 'h-8 min-w-12' : 'h-9 min-w-14')">
        <span class="flex items-center gap-2 group-focus-within:invisible group-hover:invisible">
          <NodeStatus v-if="!compact" :thread="thread" :size="13" />
          <time :datetime="at" :title="new Date(at).toLocaleString('zh-CN')" class="text-xs text-muted-foreground">{{ ago }}</time>
        </span>
        <ThreadMenu
          v-if="menu"
          :thread="thread"
          :class="cn('absolute right-0.5 opacity-0 focus-visible:opacity-100 group-focus-within:opacity-100 group-hover:opacity-100 has-[[aria-expanded=true]]:opacity-100', compact ? 'top-0' : 'top-0.5')"
          @rename="renaming = true"
        />
      </span>
    </div>
    <div v-if="!compact && (progress || delivered > 0)" class="-mt-1 flex min-w-0 items-center gap-1.5 pb-2 pl-[34px] pr-2" data-tags="">
      <template v-if="progress">
        <button
          v-if="thread.main_tasks.length"
          type="button"
          :class="cn(TAG, 'shrink-0 hover:border-input hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring', planOpen && 'border-input text-foreground')"
          :title="`主任务完成 ${progress.label}，${planOpen ? '收起' : '展开'}计划`"
          :aria-expanded="planOpen"
          data-plan-toggle=""
          @click.stop="planOpen = !planOpen"
        >
          <ListChecks class="size-3" :stroke-width="1.8" />{{ progress.label }}
        </button>
        <span v-else :class="cn(TAG, 'shrink-0')" :title="`主任务完成 ${progress.label}`"><ListChecks class="size-3" :stroke-width="1.8" />{{ progress.label }}</span>
      </template>
      <RouterLink
        v-if="delivered > 0"
        :to="`/projects/${thread.project_id}/threads/${thread.no}/deliverables`"
        :class="cn(TAG, 'shrink-0 hover:border-input hover:text-foreground')"
        data-deliverables=""
      >
        <Package class="size-3" :stroke-width="1.8" />{{ delivered }} 个交付物
      </RouterLink>
      <span v-if="step" class="min-w-0 truncate text-xs text-muted-foreground" :title="step.title" data-current-step="">{{ step.title }}</span>
    </div>
    <div v-if="!compact && planOpen && thread.main_tasks.length" class="pb-2 pl-[34px] pr-2" data-row-plan="">
      <PlanChecklist :steps="thread.main_tasks" :collapsible="false" list-class="max-h-64" />
    </div>
    <button
      v-if="nest && nest.children > 0"
      type="button"
      :aria-expanded="!subsFolded"
      :class="cn('mb-1 ml-[34px] flex items-center gap-1 rounded-md px-1 text-[11px] text-muted-foreground hover:text-foreground', compact && 'mt-0')"
      data-subs-toggle=""
      @click.stop="emit('toggleSubs')"
    >
      <ChevronDown class="size-3 transition-transform" :class="subsFolded && '-rotate-90'" :stroke-width="1.8" />{{ nest.children }} 个子讨论串
    </button>
  </div>
</template>
