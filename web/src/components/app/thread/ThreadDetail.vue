<script setup lang="ts">
// One thread, chat only (like Claude Code's thread panel): the breadcrumb header, the
// conversation between you and the lead — question cards (answered ones collapsed to your
// choice), the lead's checklist, plans, published files and the acceptance card sit where they
// happened — and the message box. 任务 / 交付物 / 成员 open in a dialog over the chat.
import type { ThreadView } from "@/api/views";
import { cn } from "@/lib/utils";
import type { ThreadViewId } from "../threadMenu";
import ThreadViewDialog from "../threadviews/ThreadViewDialog.vue";
import DetailHeader from "./DetailHeader.vue";
import type { ExpandLink } from "./detail";
import LeadComposer from "./LeadComposer.vue";
import ThreadChat from "./ThreadChat.vue";

withDefaults(
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
  }>(),
  { layout: "panel", expand: undefined, fullHref: undefined },
);
const emit = defineEmits<{ view: [view: ThreadViewId | null]; back: []; close: [] }>();
</script>

<template>
  <DetailHeader :thread="thread" :expand="expand" :full-href="fullHref" @back="emit('back')" @close="emit('close')" @view="(v) => emit('view', v)" />
  <div class="scroll-thin flex-1 overflow-y-auto">
    <div :class="cn('flex flex-col px-5 pb-6 pt-5', layout === 'page' && 'mx-auto w-full max-w-[760px] sm:px-6 sm:pt-8')">
      <ThreadChat :pid="pid" :thread="thread" :compact="layout !== 'page'" />
    </div>
  </div>
  <LeadComposer :pid="pid" :thread="thread" :class="layout === 'page' ? 'mx-auto w-full max-w-[760px] px-3 pb-3 sm:px-6 sm:pb-5' : 'px-4 pb-4'" />
  <ThreadViewDialog :pid="pid" :thread="thread" :view="view" @view="(v) => emit('view', v)" />
</template>
