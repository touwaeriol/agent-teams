<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useLegacyConversations } from "@/api/queries";
import { timeAgo } from "@/lib/utils";

const props = defineProps<{ pid: string }>();
const legacy = useLegacyConversations(() => props.pid);
const list = computed(() => legacy.data.value ?? []);
</script>

<template>
  <section v-if="list.length > 0" class="flex flex-col gap-2 rounded-xl border bg-panel p-4">
    <h2 class="text-sm font-semibold">旧对话 · {{ list.length }}</h2>
    <p class="text-xs text-muted-foreground">v1 里的对话，只读。</p>
    <RouterLink v-for="c in list" :key="c.id" :to="`/projects/${pid}/legacy/${c.id}`" class="flex justify-between gap-2 rounded-lg px-2 py-1.5 text-[13px] hover:bg-background">
      <span class="truncate">{{ c.title || "未命名对话" }}</span>
      <span class="shrink-0 text-xs text-muted-foreground">{{ timeAgo(c.created_at) }}</span>
    </RouterLink>
  </section>
</template>
