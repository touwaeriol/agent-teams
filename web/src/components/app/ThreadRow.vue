<script setup lang="ts">
// One row of the panel list: mark, "#N 标题" (PR icon when it opened one), a grey one-line
// progress, last activity on the right (⋯ menu on hover), and its newest deliverable under it.
// A pin marks a pinned thread; "提醒" a reminder that fired, "提醒 明天 09:00" one still to come.
import { Bell, GitPullRequest, Pin } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { ThreadView } from "@/api/views";
import { hasPicture } from "@/lib/artifacts";
import { fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { lastActivity, reminderLabel, shortAgo, type RowMark } from "@/lib/threadList";
import DeliverablePreview from "./DeliverablePreview.vue";
import { FILE_ICONS } from "./fileIcons";
import ItemLink from "./ItemLink.vue";
import TitleEdit from "./thread/TitleEdit.vue";
import ThreadMark from "./ThreadMark.vue";
import ThreadMenu from "./ThreadMenu.vue";

const props = defineProps<{ thread: ThreadView; line: string; menu: boolean; mark: RowMark; now: number }>();
const emit = defineEmits<{ open: [no: number] }>();
const at = computed(() => lastActivity(props.thread));
const ago = computed(() => shortAgo(at.value, props.now));
const deliverable = computed(() => props.thread.latest_deliverable ?? null);
const picture = computed(() => deliverable.value !== null && hasPicture(deliverable.value));
const renaming = ref(false);
const reminder = computed(() => (props.thread.remind_at ? `提醒 ${reminderLabel(props.thread.remind_at, props.now)}` : ""));
</script>

<template>
  <div class="group -mx-2 rounded-lg hover:bg-background" data-row="">
    <div class="flex items-start">
      <div v-if="renaming" class="flex min-w-0 flex-1 items-center gap-2.5 py-1 pl-2 pr-1">
        <ThreadMark :thread="thread" :mark="mark" />
        <TitleEdit :thread="thread" @done="renaming = false" />
      </div>
      <button v-else type="button" class="flex min-w-0 flex-1 gap-2.5 py-2 pl-2 pr-1 text-left" @click="emit('open', thread.no)">
        <ThreadMark :thread="thread" :mark="mark" />
        <span class="flex min-w-0 flex-1 flex-col gap-[3px]">
          <span class="flex min-w-0 items-center gap-1.5 text-sm font-medium">
            <Pin v-if="thread.pinned_at" class="size-3 shrink-0 rotate-45 text-muted-foreground" :stroke-width="2" aria-label="已置顶" />
            <span class="truncate">#{{ thread.no }} {{ thread.title }}</span>
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
      <span class="relative flex h-9 w-14 shrink-0 items-center justify-end pr-2">
        <time :datetime="at" :title="new Date(at).toLocaleString('zh-CN')" class="text-xs text-muted-foreground group-focus-within:invisible group-hover:invisible">{{ ago }}</time>
        <ThreadMenu
          v-if="menu"
          :thread="thread"
          class="absolute right-0.5 top-0.5 opacity-0 focus-visible:opacity-100 group-focus-within:opacity-100 group-hover:opacity-100 has-[[aria-expanded=true]]:opacity-100"
          @rename="renaming = true"
        />
      </span>
    </div>
    <div v-if="deliverable" class="ml-[15px] border-l border-border pb-2 pl-[17px]">
      <ItemLink
        v-if="picture"
        :pid="thread.project_id"
        :item="deliverable"
        :title="deliverable.title"
        class="block w-[136px] overflow-hidden rounded-lg border bg-panel shadow-[0_1px_2px_rgba(0,0,0,0.04)] hover:border-input"
      >
        <DeliverablePreview :item="deliverable" />
        <span class="sr-only">{{ deliverable.title }}</span>
      </ItemLink>
      <ItemLink
        v-else
        :pid="thread.project_id"
        :item="deliverable"
        class="inline-flex max-w-full items-center gap-1.5 rounded-md border bg-panel px-2 py-1 text-xs text-foreground/80 hover:border-input"
      >
        <component :is="FILE_ICONS[fileIcon(deliverable)]" aria-hidden="true" class="size-3.5 shrink-0" :style="{ color: deliverableKind(deliverable.kind).fg }" :stroke-width="1.8" />
        <span class="truncate">{{ deliverable.title }}</span>
      </ItemLink>
    </div>
  </div>
</template>
