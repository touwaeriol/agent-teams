<script setup lang="ts">
/** The @ references of a sent message, as chips under the bubble (title: what the employee received). */
import { AtSign } from "lucide-vue-next";
import type { MessageRefChip } from "@/api/views";

defineProps<{ refs: MessageRefChip[] | undefined }>();
const KIND: Record<string, string> = { file: "文件", thread: "讨论串", deliverable: "交付物", memory: "记忆" };
const title = (r: MessageRefChip) => (r.kind === "file" ? `${KIND.file}：${r.repo}/${r.path}${r.ref ? `@${r.ref}` : ""}` : `${KIND[r.kind]}：${r.label}`);
</script>

<template>
  <div v-if="refs?.length" class="flex flex-wrap justify-end gap-1.5 self-end" aria-label="引用">
    <span v-for="(r, i) in refs" :key="i" class="inline-flex h-6 max-w-[240px] items-center gap-1 rounded-md border bg-panel px-2 text-xs text-muted-foreground" :title="title(r)">
      <AtSign class="size-3 shrink-0" />
      <span class="truncate">{{ r.label }}</span>
    </span>
  </div>
</template>
