<script setup lang="ts">
// A thread on its own page: the same chat-only view as the right panel (header, conversation
// with you and the lead, message box), at reading width. Phones and narrow screens get it for
// /projects/:pid/threads/:no; wide screens open the panel there and this page at /full.
import { computed, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { useMe, useProject, useThreadByNo, useThreads, useWaiting } from "@/api/queries";
import ThreadDetail from "@/components/app/thread/ThreadDetail.vue";
import { detailViewOf, threadNav } from "@/components/app/thread/detail";
import type { ThreadViewId } from "@/components/app/threadMenu";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { useMedia } from "@/lib/media";
import { useReminderAlerts } from "@/lib/reminders";
import { listOrder, loadFolded } from "@/lib/threadList";
import { listFilterOf, listView } from "@/lib/threadListView";
import { threadPath, waitingIndex } from "./main/logic";

const route = useRoute();
const router = useRouter();
const pid = computed(() => String(route.params.pid ?? ""));
const no = computed(() => Number(route.params.no));
const project = useProject(pid);
const { thread, isLoading } = useThreadByNo(pid, no);
const wide = useMedia("(min-width: 1024px)");
const view = ref<ThreadViewId | null>(detailViewOf(route.params.tab));
watch(() => route.params.tab, (tab) => (view.value = detailViewOf(tab)));
const home = () => void router.push(`/projects/${pid.value}`);
/** ‹ goes to the thread list: the panel on wide screens, the phone overview's 讨论串 tab otherwise. */
const toList = () => void router.push(wide.value ? `/projects/${pid.value}` : `/projects/${pid.value}?sheet=threads`);
const threads = useThreads(pid);
const waiting = useWaiting(pid);
const me = useMe();
const full = computed(() => route.path.endsWith("/full"));
/** 上一个 / 下一个讨论串 in the panel list's order, staying on this page (full or not). */
const nav = computed(() => {
  const order = listOrder(threads.data.value ?? [], no.value, listFilterOf(listView(pid.value)), waitingIndex(waiting.data.value?.questions), loadFolded(me.data.value?.id ?? "anon"));
  return threadNav(order, no.value, (n) => `${threadPath(pid.value, n)}${full.value ? "/full" : ""}`);
});
useReminderAlerts(() => threads.data.value ?? [], (n) => void router.push(threadPath(pid.value, n)));
const expand = computed(() => (wide.value && thread.value ? { to: threadPath(pid.value, thread.value.no), label: "退出全屏", collapse: true } : undefined));
</script>

<template>
  <Loading v-if="isLoading || !project.data.value" class="h-full" />
  <EmptyState v-else-if="!thread" :title="`找不到讨论串 #${route.params.no}`" description="它可能已被归档或删除。">
    <template #action>
      <RouterLink v-slot="{ href, navigate }" :to="`/projects/${pid}`" custom>
        <el-button tag="a" :href="href" type="primary" @click="navigate">回到项目</el-button>
      </RouterLink>
    </template>
  </EmptyState>
  <div v-else class="flex h-full min-h-0 flex-col bg-panel">
    <ThreadDetail :key="thread.id" :pid="pid" :thread="thread" :view="view" :expand="expand" :nav="nav" layout="page" :closable="wide" @view="(v) => (view = v)" @back="toList" @close="home" />
  </div>
</template>
