<script setup lang="ts">
// One thread, chat only (like Claude Code's thread panel): the capsule header, the
// conversation between you and the lead — question cards (answered ones collapsed to your
// choice), the lead's checklist, plans, published files and the acceptance card sit where they
// happened — the wake-ups waiting to fire and the message box. 任务 / 交付物 / 成员 open in a dialog over the chat.
import { ref, watch } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { readLocal, writeLocal } from "@/lib/persist";
import { cn } from "@/lib/utils";
import PlanChecklist from "../plan/PlanChecklist.vue";
import type { ThreadViewId } from "../threadMenu";
import ThreadViewDialog from "../threadviews/ThreadViewDialog.vue";
import DetailHeader from "./DetailHeader.vue";
import type { ExpandLink, ThreadNav } from "./detail";
import LeadComposer from "./LeadComposer.vue";
import SubthreadStrip from "./SubthreadStrip.vue";
import ThreadChat from "./ThreadChat.vue";
import ThreadWakeups from "./ThreadWakeups.vue";

const props = withDefaults(
  defineProps<{
    pid: string;
    thread: ThreadView;
    /** The view open over the chat. */
    view: ThreadViewId | null;
    /** Expand (panel → full page) or collapse (full page → panel); omitted where it would do nothing. */
    expand?: ExpandLink;
    /** Where the full page is, for the ⋯ menu (panel only). */
    fullHref?: string;
    /** `page`: the full-page view, chat centered at reading width. */
    layout?: "panel" | "page";
    /** 上一个 / 下一个讨论串 in the list the user came from. */
    nav?: ThreadNav;
    /** Show ✕ (off on phones, where ‹ already goes back to the list). */
    closable?: boolean;
  }>(),
  { layout: "panel", expand: undefined, fullHref: undefined, nav: undefined, closable: true },
);
const emit = defineEmits<{ view: [view: ThreadViewId | null]; back: []; close: [] }>();

// A reminder that fired is seen once the thread is open: clear it (it leaves 等你处理).
const seen = useAction((tid: string) => threadActions.clearReminder(tid), { invalidate: () => [keys.threads(props.pid), keys.thread(props.thread.id)] });
watch(
  () => props.thread.reminded_at,
  (at) => at && !seen.isPending.value && seen.mutate(props.thread.id),
  { immediate: true },
);

// The plan card above the message box: folded or not is remembered per thread (open by default).
const planKey = (tid: string) => `at:plan-open:${tid}`;
const planOpen = ref(true);
watch(
  () => props.thread.id,
  (tid) => (planOpen.value = readLocal(planKey(tid)) !== "0"),
  { immediate: true },
);
function setPlanOpen(open: boolean) {
  planOpen.value = open;
  writeLocal(planKey(props.thread.id), open ? null : "0");
}
</script>

<template>
  <DetailHeader :thread="thread" :expand="expand" :full-href="fullHref" :nav="nav" :closable="closable" @back="emit('back')" @close="emit('close')" @view="(v) => emit('view', v)" />
  <SubthreadStrip :pid="pid" :thread="thread" />
  <div class="scroll-thin min-h-0 flex-1 overflow-y-auto">
    <div :class="cn('at-reading flex min-w-0 flex-col px-5 pb-6 pt-5', layout === 'page' && 'sm:px-6 sm:pt-8')">
      <ThreadChat :pid="pid" :thread="thread" :compact="layout !== 'page'" />
    </div>
  </div>
  <ThreadWakeups :pid="pid" :thread-id="thread.id" :class="cn('at-reading shrink-0 mb-2', layout === 'page' ? 'mx-3 sm:mx-6' : 'mx-4')" />
  <div v-if="thread.main_tasks.length" :class="cn('at-reading shrink-0 pb-2', layout === 'page' ? 'px-3 sm:px-6' : 'px-4')" data-plan-dock>
    <PlanChecklist :steps="thread.main_tasks" :open="planOpen" list-class="max-h-[min(40vh,280px)]" @update:open="setPlanOpen" />
  </div>
  <LeadComposer :pid="pid" :thread="thread" :class="cn('at-reading', layout === 'page' ? 'px-3 pb-3 sm:px-6 sm:pb-5' : 'px-4 pb-4')" />
  <ThreadViewDialog :pid="pid" :thread="thread" :view="view" @view="(v) => emit('view', v)" />
</template>
