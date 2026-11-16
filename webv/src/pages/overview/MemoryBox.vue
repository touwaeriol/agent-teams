<script setup lang="ts">
import { RouterLink } from "vue-router";
import type { OverviewView } from "@/api/views";

defineProps<{ pid: string; overview: OverviewView }>();
</script>

<template>
  <section class="flex flex-col gap-2 rounded-xl border bg-panel p-4">
    <h2 class="text-sm font-semibold">项目记忆</h2>
    <p class="text-[13px] leading-[22px] text-[#4A4843]">讨论串结束时组长写总结，统筹据此更新，开新讨论串时自动带上。</p>
    <ul v-if="overview.memory.length > 0" class="list-disc pl-[18px] text-[13px] leading-[22px]">
      <li v-for="m in overview.memory" :key="m.id">{{ m.text }}</li>
    </ul>
    <span v-else class="text-[13px] text-muted-foreground">还没有记忆。</span>
    <RouterLink :to="`/projects/${pid}/settings/memory`" class="text-[13px] underline-offset-2 hover:underline">查看和编辑</RouterLink>
  </section>
</template>
