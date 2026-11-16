<script setup lang="ts">
import { ArrowDown, ArrowUp, X } from "lucide-vue-next";
import { computed } from "vue";
import type { GitConnectionView } from "@/api/views";
import { connectionLabel, type RepoRef } from "./repo";

const props = defineProps<{ item: RepoRef; index: number; count: number; connections: GitConnectionView[] }>();
const emit = defineEmits<{ move: [delta: number]; remove: [] }>();
const conn = computed(() => props.connections.find((c) => c.id === props.item.git_connection_id));
const BTN = "flex size-8 shrink-0 items-center justify-center rounded-lg hover:bg-raised disabled:pointer-events-none disabled:opacity-50";
</script>

<template>
  <li class="flex items-center gap-2 px-3 py-2">
    <span class="w-5 text-[12px] tabular-nums text-muted-foreground">{{ index + 1 }}</span>
    <span class="min-w-0 flex-1 truncate font-mono text-[13px]">{{ item.repo }}</span>
    <span class="truncate text-[12px] text-muted-foreground max-sm:hidden">{{ conn ? connectionLabel(conn) : "连接已删除" }}</span>
    <button type="button" :class="BTN" :aria-label="`上移 ${item.repo}`" :disabled="index === 0" @click="emit('move', -1)"><ArrowUp class="size-4" /></button>
    <button type="button" :class="BTN" :aria-label="`下移 ${item.repo}`" :disabled="index === count - 1" @click="emit('move', 1)"><ArrowDown class="size-4" /></button>
    <button type="button" :class="BTN" :aria-label="`移除 ${item.repo}`" @click="emit('remove')"><X class="size-4" /></button>
  </li>
</template>
