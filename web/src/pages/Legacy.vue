<script setup lang="ts">
// A v1 conversation, read only (api.md §3.13): messages and its task list.
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { client, unwrap } from "@/api/client";
import { flattenMessages, useLegacyConversations, useMessages } from "@/api/queries";
import Chat from "@/components/app/Chat.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";

const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const cid = computed(() => String(route.params.cid ?? ""));
const list = useLegacyConversations(pid);
const messages = useMessages(cid);
const tasks = useQuery({
  queryKey: computed(() => ["c", cid.value, "legacy-tasks"]),
  queryFn: () => unwrap(client.GET("/api/conversations/{cid}/tasks", { params: { path: { cid: cid.value } } })),
});
const title = computed(() => list.data.value?.find((c) => c.id === cid.value)?.title || "旧对话");
const taskRows = computed(() => (flattenMessages(messages.data.value).length > 0 ? (tasks.data.value ?? []) : []));
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[760px] flex-col gap-5">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <RouterLink :to="`/projects/${pid}/overview`" class="text-xs text-muted-foreground hover:text-foreground">项目首页 / 旧对话</RouterLink>
          <h1 class="text-2xl font-semibold">{{ title }}</h1>
          <p class="text-[13px] text-muted-foreground">v1 时期的对话，只读。新的需求请在项目里和统筹说。</p>
        </div>
      </div>
      <Loading v-if="messages.isLoading.value" />
      <Chat v-else :cid="cid" compact>
        <template #empty><p class="text-[13px] text-muted-foreground">没有消息。</p></template>
      </Chat>
      <section v-if="taskRows.length > 0" class="flex flex-col gap-2 rounded-[14px] border bg-panel p-5">
        <h2 class="text-[15px] font-semibold">任务</h2>
        <div v-for="t in taskRows" :key="t.id" class="flex gap-3 border-t border-[#F1EFEA] pt-2 text-[13px]">
          <span class="font-mono text-muted-foreground">{{ t.id }}</span>
          <span class="font-mono">{{ t.assignee }}</span>
          <span class="min-w-0 flex-1 truncate">{{ t.instruction }}</span>
          <span class="text-muted-foreground">{{ t.status }}</span>
        </div>
      </section>
    </div>
  </main>
</template>
