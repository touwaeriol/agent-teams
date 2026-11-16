<script setup lang="ts">
// Main board: chat with the project coordinator in the middle, the thread list (→ thread
// detail) in the resizable right panel. Phones get thread chips instead.
// Not ported yet (PanelPending marks the spots): ThreadDetail, LibraryBrowser, SchedulesDialog,
// ProjectSettingsDialog — see README.md "Porting status".
import { computed, ref, watchEffect } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useCoordinator, useMe, useProject, useThreads } from "@/api/queries";
import SidePanel from "@/components/app/SidePanel.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import InfoDialog from "@/components/common/InfoDialog.vue";
import Loading from "@/components/common/Loading.vue";
import { errorText } from "@/api/mutations";
import CoordinatorChat from "./main/CoordinatorChat.vue";
import CoordinatorComposer from "./main/CoordinatorComposer.vue";
import MainHeader from "./main/MainHeader.vue";
import PanelPending from "./main/PanelPending.vue";
import PanelList from "./main/PanelList.vue";
import ThreadChips from "./main/ThreadChips.vue";
import { sidePanelOf, threadPath, type SidePanel as Panel } from "./main/logic";

const props = withDefaults(defineProps<{ openNo?: number | null }>(), { openNo: null });
const route = useRoute();
const router = useRouter();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const coordinator = useCoordinator(pid);
const threads = useThreads(pid);
const me = useMe();
const list = computed(() => threads.data.value ?? []);
const panel = computed(() => sidePanelOf(route.query.panel));
const schedules = ref(false);
const setPanel = (p: Panel) => void router.replace({ query: p === "threads" ? {} : { panel: p } });
const openThread = (no: number) => void router.push(threadPath(pid.value, no));

// Old links (?thread=N) open the thread route.
watchEffect(() => {
  const legacy = Number(route.query.thread);
  if (legacy) void router.replace(threadPath(pid.value, legacy));
});
</script>

<template>
  <EmptyState v-if="project.error.value" title="找不到这个项目" :description="errorText(project.error.value)" />
  <Loading v-else-if="!project.data.value" class="h-full" />
  <div v-else class="flex h-full min-h-0 flex-1">
    <main class="flex min-h-0 min-w-0 flex-1 flex-col">
      <MainHeader :project="project.data.value" :coordinator="coordinator.data.value" :threads="list" :panel="panel" @panel="setPanel" @schedules="schedules = true" />
      <ThreadChips :pid="pid" :threads="list" />
      <div class="flex-1 overflow-y-auto">
        <div class="mx-auto flex max-w-[720px] flex-col px-4 pb-6 pt-4 sm:px-6 sm:pt-8">
          <CoordinatorChat :pid="pid" :coordinator="coordinator.data.value" :threads="list" @open-thread="openThread" />
        </div>
      </div>
      <CoordinatorComposer :pid="pid" :coordinator="coordinator.data.value" />
    </main>
    <SidePanel v-if="panel === 'threads'" :user-id="me.data.value?.id ?? 'anon'" label="讨论串" handle-label="调整讨论串宽度">
      <PanelPending v-if="props.openNo" :title="`讨论串 #${props.openNo}`" source="web/src/components/app/ThreadDetail.tsx" @close="router.push(`/projects/${pid}`)" />
      <PanelList v-else :pid="pid" :threads="list" :loading="threads.isLoading.value" @open="openThread" />
    </SidePanel>
    <aside
      v-if="panel === 'library'"
      aria-label="资料库"
      class="fixed inset-0 z-30 flex flex-col bg-panel lg:static lg:z-auto lg:h-full lg:w-[min(820px,56vw)] lg:min-w-[420px] lg:shrink-0 lg:border-l"
    >
      <PanelPending title="资料库" source="web/src/components/app/Library.tsx" @close="setPanel('threads')" />
    </aside>
    <InfoDialog v-model:open="schedules" title="定时任务" description="定时任务对话框还在迁移，React 版见 web/src/components/app/Schedules.tsx。" />
  </div>
</template>
