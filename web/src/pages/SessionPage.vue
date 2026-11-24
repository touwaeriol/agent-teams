<script setup lang="ts">
// A session (/sessions/:pid): the conversation with its one employee in the middle, 工作现场 on
// the right (a drawer on narrow screens). 结束会话 completes the thread; the conversation stays and
// the next message reopens it.
import { CodeXml, PanelRight } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useProject, useThreadByNo } from "@/api/queries";
import StatusPill from "@/components/app/StatusPill.vue";
import SessionWorkspace from "@/components/app/session/SessionWorkspace.vue";
import LeadComposer from "@/components/app/thread/LeadComposer.vue";
import ThreadChat from "@/components/app/thread/ThreadChat.vue";
import ThreadWakeups from "@/components/app/thread/ThreadWakeups.vue";
import PlanChecklist from "@/components/app/plan/PlanChecklist.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { useMedia } from "@/lib/media";
import { threadLook } from "@/lib/present";

const route = useRoute();
const router = useRouter();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const { thread, isLoading } = useThreadByNo(pid, 1);
const wide = useMedia("(min-width: 1024px)");
const drawer = ref(false);
const look = computed(() => (thread.value ? threadLook(thread.value) : null));
const ended = computed(() => thread.value?.status === "done" || thread.value?.status === "failed");
// A project that is not a session has its own board.
watch(
  () => project.data.value?.kind,
  (kind) => kind && kind !== "session" && void router.replace(`/projects/${pid.value}`),
  { immediate: true },
);
const invalidate = () => (thread.value ? [keys.threads(pid.value), keys.thread(thread.value.id), keys.projects] : []);
const finish = useAction(() => threadActions.complete(thread.value!.id), { invalidate, success: "会话已结束，发消息可以继续" });
</script>

<template>
  <Loading v-if="project.isLoading.value || isLoading" class="h-full" />
  <EmptyState v-else-if="!project.data.value || !thread" title="找不到这个会话" description="它可能已被删除。">
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
        <StatusPill v-if="look" :tone="look.tone" :label="look.label" :spin="look.spin" />
        <div class="ml-auto flex items-center gap-1.5">
          <ConfirmAction v-if="!ended" title="结束这个会话？" description="员工会停下，对话记录会保留；之后再发消息可以继续。" confirm-label="结束会话" variant="primary" size="small" @confirm="finish.mutate()">
            结束会话
          </ConfirmAction>
          <el-button v-if="!wide" size="small" aria-label="工作现场" @click="drawer = true"><PanelRight class="size-4" /></el-button>
        </div>
      </header>
      <div class="scroll-thin min-h-0 flex-1 overflow-y-auto">
        <div class="at-reading flex min-w-0 flex-col px-5 pb-6 pt-5 sm:px-6 sm:pt-8">
          <ThreadChat :pid="pid" :thread="thread" :compact="false" session />
        </div>
      </div>
      <ThreadWakeups :pid="pid" :thread-id="thread.id" class="at-reading mx-3 mb-2 shrink-0 sm:mx-6" />
      <div v-if="thread.main_tasks.length" class="at-reading shrink-0 px-3 pb-2 sm:px-6">
        <PlanChecklist :steps="thread.main_tasks" :open="true" list-class="max-h-[min(40vh,280px)]" />
      </div>
      <LeadComposer :pid="pid" :thread="thread" session class="at-reading px-3 pb-3 sm:px-6 sm:pb-5" />
    </div>
    <aside v-if="wide" aria-label="工作现场" class="scroll-thin w-[340px] shrink-0 overflow-y-auto border-l bg-sidebar">
      <h2 class="px-4 pt-4 text-[13px] font-semibold">工作现场</h2>
      <SessionWorkspace :project="project.data.value" :thread="thread" />
    </aside>
    <el-drawer v-else v-model="drawer" title="工作现场" size="340px">
      <SessionWorkspace :project="project.data.value" :thread="thread" />
    </el-drawer>
  </div>
</template>
