<script setup lang="ts">
// 节点: totals, then one card per machine. Adding a node or rotating its token shows the one-time
// commands (host, docker run, compose).
import { computed, ref } from "vue";
import { useNodes } from "@/api/queries";
import type { Enrollment } from "@/api/views";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import AddNode from "./nodes/AddNode.vue";
import EnrollmentDialog from "./nodes/EnrollmentDialog.vue";
import NodeCard from "./nodes/NodeCard.vue";
import Scheduling from "./nodes/Scheduling.vue";
import { dockerGuideUrl } from "./nodes/enroll";
import { nodeStats } from "./nodes/nodes";

const nodes = useNodes();
const list = computed(() => nodes.data.value ?? []);
const stats = computed(() => nodeStats(list.value));
const adding = ref(false);
const enrollment = ref<{ title: string; data: Enrollment } | null>(null);
const guide = dockerGuideUrl();
const shown = computed({ get: () => Boolean(enrollment.value), set: (v: boolean) => !v && (enrollment.value = null) });
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[1040px] flex-col gap-5">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold">节点</h1>
            <p class="text-[13px] text-muted-foreground">每台机器跑一个守护进程主动连上来。统筹、组长和成员都运行在这里，用的是机器上 Claude Code / Codex / Antigravity 的登录。</p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <a :href="guide" target="_blank" rel="noopener" class="text-[13px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline" title="公开文档，可以直接发给 AI">Docker 部署说明</a>
          <el-button type="primary" class="h-[38px]" @click="adding = true">添加节点</el-button>
        </div>
      </header>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div v-for="x in stats" :key="x.k" class="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
          <span class="text-xs text-muted-foreground">{{ x.k }}</span>
          <span class="text-xl font-semibold">{{ x.v }}</span>
        </div>
      </div>
      <Scheduling />
      <Loading v-if="nodes.isLoading.value" />
      <EmptyState v-if="nodes.data.value && list.length === 0" title="还没有节点" description="点“添加节点”，在你的机器上运行一行安装命令，或用 Docker / Docker Compose 启动，守护进程会连上来。" />
      <NodeCard v-for="n in list" :key="n.id" :node="n" @token="(data: Enrollment) => (enrollment = { title: `${n.name} 的新令牌`, data })" />
    </div>
    <AddNode v-model:open="adding" @created="(name: string, data: Enrollment) => (enrollment = { title: `节点 ${name} 已创建`, data })" />
    <EnrollmentDialog v-model:open="shown" :title="enrollment?.title ?? ''" :data="enrollment?.data ?? null" />
  </main>
</template>
