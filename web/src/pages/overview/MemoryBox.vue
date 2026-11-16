<script setup lang="ts">
import { RouterLink } from "vue-router";
import type { OverviewView } from "@/api/views";
import { timeAgo } from "@/lib/utils";

defineProps<{ pid: string; overview: OverviewView }>();
</script>

<template>
  <section class="flex flex-col gap-2 rounded-xl border bg-panel p-4">
    <h2 class="text-sm font-semibold">项目记忆</h2>
    <p class="text-[13px] leading-[22px] text-[#4A4843]">统筹和组长边做边记：MEMORY.md 是索引，每个新会话都会读到；细节在各个记忆文件里。</p>
    <ul v-if="overview.memory.length > 0" class="flex flex-col gap-1 text-[13px] leading-[22px]">
      <li v-for="m in overview.memory" :key="m.name" class="flex gap-2">
        <span class="shrink-0 font-mono">{{ m.name }}</span>
        <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ m.description || `更新于 ${timeAgo(m.updated_at)}` }}</span>
      </li>
    </ul>
    <span v-else class="text-[13px] text-muted-foreground">还没有记忆。</span>
    <RouterLink :to="`/projects/${pid}/settings/memory`" class="text-[13px] underline-offset-2 hover:underline">查看和编辑</RouterLink>
  </section>
</template>
