<script setup lang="ts">
// A session (/sessions/:pid): the conversation with its one employee in the middle; 会话信息 (环境 +
// 上下文) on the right, closed by default and toggled from the header (a collapsible side panel on
// wide screens, a drawer on narrow ones). A finished round reads 已完成 and asks 问题解决了吗？
// under the last reply; 已解决 completes the thread (the next message reopens it).
import { CodeXml, PanelRight, X } from "lucide-vue-next";
import { computed, nextTick, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { useQueryClient } from "@tanstack/vue-query";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useProject, useThreadByNo } from "@/api/queries";
import StatusPill from "@/components/app/StatusPill.vue";
import SessionConfirm from "@/components/app/session/SessionConfirm.vue";
import SessionWorkspace from "@/components/app/session/SessionWorkspace.vue";
import LeadComposer from "@/components/app/thread/LeadComposer.vue";
import ThreadChat from "@/components/app/thread/ThreadChat.vue";
import ThreadWakeups from "@/components/app/thread/ThreadWakeups.vue";
import PlanChecklist from "@/components/app/plan/PlanChecklist.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { useMedia } from "@/lib/media";
import { threadPhase } from "@/lib/phase";
import { threadLook } from "@/lib/present";

const route = useRoute();
const router = useRouter();
const qc = useQueryClient();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const { thread, isLoading } = useThreadByNo(pid, 1);
const wide = useMedia("(min-width: 1024px)");
/** 会话信息: closed by default on every screen. */
const panel = ref(false);
// Older servers leave solo out: this page only shows sessions.
const view = computed(() => (thread.value ? { ...thread.value, solo: true } : null));
const look = computed(() => (view.value ? threadLook(view.value) : null));
const finished = computed(() => (view.value ? threadPhase(view.value).id === "finished" : false));
// A project that is not a session has its own board.
watch(
  () => project.data.value?.kind,
  (kind) => kind && kind !== "session" && void router.replace(`/projects/${pid.value}`),
  { immediate: true },
);
// The sidebar marks sessions 进行中 / 已完成 / 已解决 from the project list: follow this one's changes.
watch(
  () => look.value?.label,
  (now, before) => before && now !== before && void qc.invalidateQueries({ queryKey: keys.projects }),
);
const invalidate = () => (thread.value ? [keys.threads(pid.value), keys.thread(thread.value.id), keys.projects] : []);
const resolve = useAction(() => threadActions.complete(thread.value!.id), { invalidate, success: "已标记为已解决，之后发消息可以继续" });
/** 还没有，继续说: the cursor goes to the message box. */
function goOn() {
  void nextTick(() => document.getElementById(`lead-${thread.value?.id}`)?.focus());
}
</script>

<template>
  <Loading v-if="project.isLoading.value || isLoading" class="h-full" />
  <EmptyState v-else-if="!project.data.value || !thread || !view" title="找不到这个会话" description="它可能已被删除。">
    <template #action>
      <RouterLink v-slot="{ href, navigate }" to="/sessions/new" custom>
        <el-button tag="a" :href="href" type="primary" @click="navigate">新建会话</el-button>
      </RouterLink>
    </template>
  </EmptyState>
  <div v-else class="flex h-full min-h-0">
    <div class="flex min-w-0 flex-1 flex-col bg-panel">
      <header class="flex h-14 shrink-0 items-center gap-2.5 border-b px-4">
        <CodeXml class="size-4 shrink-0 text-muted-foreground" />
        <h1 class="min-w-0 truncate text-[15px] font-semibold">{{ project.data.value.name }}</h1>
        <StatusPill v-if="look" :tone="look.tone" :label="look.label" :spin="look.spin" :icon="look.icon" data-testid="session-status" />
        <div class="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            :aria-label="panel ? '隐藏会话信息' : '显示会话信息'"
            :title="panel ? '隐藏会话信息' : '显示会话信息'"
            :aria-pressed="panel"
            :class="['flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground', panel && wide ? 'bg-accent text-foreground' : '']"
            data-testid="panel-toggle"
            @click="panel = !panel"
          >
            <PanelRight class="size-4" />
          </button>
        </div>
      </header>
      <div class="scroll-thin min-h-0 flex-1 overflow-y-auto">
        <div class="at-reading flex min-w-0 flex-col px-5 pb-6 pt-5 sm:px-6 sm:pt-8">
          <ThreadChat :pid="pid" :thread="thread" :compact="false" session />
          <SessionConfirm v-if="finished" :pending="resolve.isPending.value" @resolve="resolve.mutate()" @continue="goOn" />
        </div>
      </div>
      <ThreadWakeups :pid="pid" :thread-id="thread.id" class="at-reading mx-3 mb-2 shrink-0 sm:mx-6" />
      <div v-if="thread.main_tasks.length" class="at-reading shrink-0 px-3 pb-2 sm:px-6">
        <PlanChecklist :steps="thread.main_tasks" :open="true" list-class="max-h-[min(40vh,280px)]" />
      </div>
      <LeadComposer :pid="pid" :thread="thread" session class="at-reading px-3 pb-3 sm:px-6 sm:pb-5" />
    </div>
    <aside v-if="wide && panel" aria-label="会话信息" class="flex w-[340px] shrink-0 flex-col border-l bg-sidebar" data-testid="session-panel">
      <div class="flex h-14 shrink-0 items-center justify-between border-b px-4">
        <h2 class="text-[13px] font-semibold">会话信息</h2>
        <button type="button" aria-label="关闭会话信息" title="关闭" class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground" @click="panel = false">
          <X class="size-4" />
        </button>
      </div>
      <div class="scroll-thin min-h-0 flex-1 overflow-y-auto">
        <SessionWorkspace :project="project.data.value" :thread="view" />
      </div>
    </aside>
    <el-drawer v-if="!wide" v-model="panel" title="会话信息" size="min(340px, 90vw)">
      <SessionWorkspace :project="project.data.value" :thread="view" />
    </el-drawer>
  </div>
</template>
