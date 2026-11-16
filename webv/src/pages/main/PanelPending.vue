<script setup lang="ts">
// Stands in for a panel whose content has not been ported from web/ yet (thread detail →
// components/app/ThreadDetail.tsx, library → components/app/Library.tsx). Replace it in Main.vue.
import { X } from "lucide-vue-next";
import EmptyState from "@/components/common/EmptyState.vue";

defineProps<{ title: string; source: string }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <div class="flex h-14 shrink-0 items-center justify-between border-b border-[#EFEDE8] px-5">
    <span class="font-semibold">{{ title }}</span>
    <button type="button" aria-label="关闭" class="flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised" @click="emit('close')">
      <X class="size-4" />
    </button>
  </div>
  <div class="p-5">
    <EmptyState title="这部分还在迁移" :description="`React 版见 ${source}。`" />
  </div>
</template>
