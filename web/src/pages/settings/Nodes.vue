<script setup lang="ts">
// 节点: totals, then one card per machine. Adding a node or rotating its token shows the one-time
// install command.
import { computed, ref } from "vue";
import { useNodes } from "@/api/queries";
import type { Enrollment } from "@/api/views";
import CopyBlock from "@/components/common/CopyBlock.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import InfoDialog from "@/components/common/InfoDialog.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import AddNode from "./nodes/AddNode.vue";
import NodeCard from "./nodes/NodeCard.vue";
import { nodeStats } from "./nodes/nodes";

const nodes = useNodes();
const list = computed(() => nodes.data.value ?? []);
const stats = computed(() => nodeStats(list.value));
const adding = ref(false);
const enrollment = ref<{ title: string; data: Enrollment } | null>(null);
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
        <el-button type="primary" class="h-[38px]" @click="adding = true">添加节点</el-button>
      </header>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div v-for="x in stats" :key="x.k" class="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
          <span class="text-xs text-muted-foreground">{{ x.k }}</span>
          <span class="text-xl font-semibold">{{ x.v }}</span>
        </div>
      </div>
      <Loading v-if="nodes.isLoading.value" />
      <EmptyState v-if="nodes.data.value && list.length === 0" title="还没有节点" description="点“添加节点”，在你的机器上运行一行安装命令，守护进程会连上来。" />
      <NodeCard v-for="n in list" :key="n.id" :node="n" @token="(data: Enrollment) => (enrollment = { title: `${n.name} 的新令牌`, data })" />
    </div>
    <AddNode v-model:open="adding" @created="(name: string, data: Enrollment) => (enrollment = { title: `节点 ${name} 已创建`, data })" />
    <InfoDialog v-model:open="shown" :title="enrollment?.title ?? ''" description="在节点机器上运行下面的命令。令牌只显示这一次。">
      <CopyBlock v-if="enrollment" :text="enrollment.data.install" />
    </InfoDialog>
  </main>
</template>
