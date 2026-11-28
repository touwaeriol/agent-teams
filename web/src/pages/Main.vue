<script setup lang="ts">
// Main board: chat with the project coordinator in the middle, the thread list (→ thread
// detail) in the resizable right panel. Phones get thread chips and the project overview
// (?sheet=threads|library|schedules).
// /projects/:pid/settings[/:section] opens the project settings dialog over the board.
import { $t } from "@/i18n";
import { computed, nextTick, ref, watch, watchEffect } from "vue";
import { MessagesSquare } from "lucide-vue-next";
import { useRoute, useRouter } from "vue-router";
import { flattenMessages, useCoordinator, useMe, useMessages, useProject, useThreads } from "@/api/queries";
import type { ForwardView } from "@/api/views";
import ForwardBar from "@/components/app/forward/ForwardBar.vue";
import ForwardDialog from "@/components/app/forward/ForwardDialog.vue";
import { isSelecting, selection, stopSelecting } from "@/lib/forward";
import SidePanel from "@/components/app/SidePanel.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import SchedulesDialog from "@/components/app/schedules/SchedulesDialog.vue";
import Loading from "@/components/common/Loading.vue";
import { errorText } from "@/api/mutations";
import { sectionOf, type SettingsSection } from "@/lib/projectSettings";
import ProjectSettingsDialog from "./projectSettings/ProjectSettingsDialog.vue";
import CoordinatorChat from "./main/CoordinatorChat.vue";
import CoordinatorComposer from "./main/CoordinatorComposer.vue";
import MainHeader from "./main/MainHeader.vue";
import LibraryBrowser from "@/components/app/deliverables/LibraryBrowser.vue";
import type { ThreadViewId } from "@/components/app/threadMenu";
import PanelDetail from "./main/PanelDetail.vue";
import PanelList from "./main/PanelList.vue";
import MobileOverview from "./main/MobileOverview.vue";
import ThreadChips from "./main/ThreadChips.vue";
import { COORDINATOR_COMPACT, PANEL_DEFAULT, PANEL_MIN, PANEL_WIDE, roomForChat } from "@/lib/panelWidth";
import { useElementWidth, usePanelWidth } from "@/components/app/usePanelWidth";
import { overviewTabOf, sidePanelOf, threadPath, type OverviewTab, type SidePanel as Panel } from "./main/logic";
import { useReminderAlerts } from "@/lib/reminders";

const props = withDefaults(defineProps<{ openNo?: number | null; view?: ThreadViewId | null }>(), { openNo: null, view: null });
const route = useRoute();
const router = useRouter();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
// A session has no coordinator board: its address is its own page.
watch(
  () => project.data.value?.kind,
  (kind) => kind === "session" && void router.replace(`/sessions/${pid.value}`),
  { immediate: true },
);
const coordinator = useCoordinator(pid);
const threads = useThreads(pid);
const me = useMe();
const list = computed(() => threads.data.value ?? []);
const panel = computed(() => sidePanelOf(route.query.panel));
const schedules = ref(false);
const sheet = computed(() => overviewTabOf(route.query.sheet));
const setSheet = (t: OverviewTab | null) => void router.replace({ query: t ? { sheet: t } : {} });
const settings = computed(() => /^\/projects\/[^/]+\/settings(\/|$)/.test(route.path));
const section = computed(() => sectionOf(typeof route.params.section === "string" ? route.params.section : undefined));
const setSection = (s: SettingsSection) => void router.replace(`/projects/${pid.value}/settings/${s}`);
const setPanel = (p: Panel) => void router.replace({ query: p === "threads" ? {} : { panel: p } });
// Panel width: one remembered width for the list and an open thread (opening or leaving a thread
// keeps the panel's edge where it is), clamped to the board: the area right of the project
// sidebar, measured live, less a strip of the coordinator chat.
const userId = () => me.data.value?.id ?? "anon";
const boardEl = ref<HTMLElement | null>(null);
const board = useElementWidth(boardEl);
const panelWidth = usePanelWidth(userId, board);
/** The coordinator chat is squeezed to a strip: clip it at its own width and make it a way back. */
const peek = computed(() => panel.value === "threads" && board.value - panelWidth.width.value < COORDINATOR_COMPACT);
/** The strip goes back to the coordinator chat: the thread closes and the panel narrows to give the chat room. */
const backToChat = () => {
  panelWidth.set(roomForChat(panelWidth.width.value, board.value));
  if (props.openNo) void router.push(`/projects/${pid.value}`);
};
// The strip makes the coordinator chat inert; focus inside it would be dropped, so hand it to the panel.
const mainEl = ref<HTMLElement | null>(null);
watch(peek, (on) => {
  if (!on || !mainEl.value?.contains(document.activeElement)) return;
  void nextTick(() => boardEl.value?.querySelector<HTMLElement>(`aside[aria-label="${$t("讨论串")}"]`)?.focus({ preventScroll: true }));
});
/** The panel is expanded (the list's expand button then shrinks it back to the default). */
const isWide = computed(() => panelWidth.width.value >= Math.min(PANEL_WIDE, panelWidth.max.value) && panelWidth.width.value > PANEL_DEFAULT);
const openThread = (no: number) => void router.push(threadPath(pid.value, no));
useReminderAlerts(() => list.value, openThread);
// Forwarding project-chat messages: the bar replaces the composer while selecting.
const cid = computed(() => coordinator.data.value?.conversation_id);
const chat = useMessages(cid);
const selecting = computed(() => isSelecting(cid.value));
const picking = ref(false);
const forwarded = (v: ForwardView) => {
  stopSelecting();
  openThread(v.thread_no);
};
const setView = (v: ThreadViewId | null) => props.openNo && void router.replace(threadPath(pid.value, props.openNo, v));

// Old links (?thread=N) open the thread route.
watchEffect(() => {
  const legacy = Number(route.query.thread);
  if (legacy) void router.replace(threadPath(pid.value, legacy));
});
</script>

<template>
  <EmptyState v-if="project.error.value" :title="$t('找不到这个项目')" :description="errorText(project.error.value)" />
  <Loading v-else-if="!project.data.value" class="h-full" />
  <div v-else ref="boardEl" class="flex h-full min-h-0 min-w-0 flex-1">
    <main ref="mainEl" :class="['relative flex min-h-0 min-w-0 flex-1', peek && 'overflow-clip']" :data-peek="peek ? '' : undefined">
      <div :class="['flex min-h-0 min-w-0 flex-1 flex-col', peek && 'min-w-[720px]']" :inert="peek || undefined">
        <MainHeader :project="project.data.value" :coordinator="coordinator.data.value" :threads="list" :panel="panel" @panel="setPanel" @schedules="schedules = true" @overview="setSheet('threads')" />
        <ThreadChips :pid="pid" :threads="list" />
        <div class="flex-1 overflow-y-auto">
          <div class="mx-auto flex max-w-[720px] flex-col px-4 pb-6 pt-4 sm:px-6 sm:pt-8">
            <CoordinatorChat :pid="pid" :coordinator="coordinator.data.value" :threads="list" @open-thread="openThread" />
          </div>
        </div>
        <ForwardBar v-if="selecting" :count="selection.ids.length" @forward="picking = true" @cancel="stopSelecting" />
        <CoordinatorComposer v-else :pid="pid" :coordinator="coordinator.data.value" />
        <ForwardDialog v-if="cid" v-model:open="picking" :cid="cid" :ids="selection.ids" :messages="flattenMessages(chat.data.value)" :threads="list" @done="forwarded" />
      </div>
      <button
        v-if="peek"
        type="button"
        :title="$t('回到项目统筹会话')"
        :aria-label="$t('回到项目统筹会话')"
        class="group absolute inset-0 z-[1] flex h-full w-full cursor-pointer flex-col items-center bg-background/70 pt-16 text-muted-foreground transition-colors hover:bg-background/45 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[#2F6FDB]"
        data-testid="coordinator-peek"
        @click="backToChat"
      >
        <span class="flex flex-col items-center gap-2 rounded-full border bg-panel px-1.5 py-2.5 shadow-sm transition-colors group-hover:border-[#2F6FDB]/50 group-hover:text-[#2F6FDB]">
          <MessagesSquare :size="16" :stroke-width="1.8" />
          <span class="text-[12px] leading-4 tracking-[2px] [writing-mode:vertical-rl]">{{ $t("项目统筹") }}</span>
        </span>
      </button>
    </main>
    <SidePanel
      v-if="panel === 'threads'"
      :width="panelWidth.width.value"
      :min="PANEL_MIN"
      :max="panelWidth.max.value"
      :label="$t('讨论串')"
      :handle-label="$t('调整讨论串宽度')"
      @change="panelWidth.set"
      @reset="panelWidth.reset"
    >
      <PanelDetail
        v-if="props.openNo"
        :pid="pid"
        :no="props.openNo"
        :view="props.view"
        :threads="list"
        :loading="threads.isLoading.value"
        :user-id="me.data.value?.id ?? 'anon'"
        @view="setView"
        @back="router.push(`/projects/${pid}`)"
        @close="router.push(`/projects/${pid}?panel=none`)"
      />
      <PanelList
        v-else
        :pid="pid"
        :threads="list"
        :loading="threads.isLoading.value"
        :user-id="me.data.value?.id ?? 'anon'"
        :expanded="isWide"
        @open="openThread"
        @expand="panelWidth.set(isWide ? PANEL_DEFAULT : PANEL_WIDE)"
      />
    </SidePanel>
    <aside
      v-if="panel === 'library'"
      :aria-label="$t('资料库')"
      class="fixed inset-0 z-30 flex flex-col bg-panel lg:static lg:z-auto lg:h-full lg:w-[min(820px,56vw)] lg:min-w-[420px] lg:shrink-0 lg:border-l"
    >
      <LibraryBrowser :pid="pid" variant="panel" @close="setPanel('threads')" />
    </aside>
    <MobileOverview
      v-if="sheet"
      :pid="pid"
      :tab="sheet"
      :threads="list"
      :loading="threads.isLoading.value"
      :user-id="me.data.value?.id ?? 'anon'"
      @tab="setSheet"
      @open="openThread"
      @close="setSheet(null)"
    />
    <SchedulesDialog v-model:open="schedules" :pid="pid" />
    <ProjectSettingsDialog v-if="settings" :project="project.data.value" :section="section" @section="setSection" @close="router.push(`/projects/${pid}`)" />
  </div>
</template>
