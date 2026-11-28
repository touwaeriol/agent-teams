<script setup lang="ts">
// MEMORY.md and the topic files of one memory: a project's (pid) or my own (pid null, every
// project reads it). Files open in MemoryFileDialog to read, edit or delete.
import { $t } from "@/i18n";
import { Plus } from "lucide-vue-next";
import { computed, ref } from "vue";
import Loading from "@/components/common/Loading.vue";
import { timeAgo } from "@/lib/utils";
import { MEMORY_INDEX, memorySource, memoryWriter, splitMemory } from "./memory";
import MemoryFileDialog from "./MemoryFileDialog.vue";

// The source is picked once: key the component by pid where the pid can change.
const props = defineProps<{ pid: string | null; label: string; heading: string }>();
const memory = memorySource(props.pid).useList();
const split = computed(() => splitMemory(memory.data.value));
/** The open dialog: a file name, "" for a new file, null when closed. */
const opened = ref<string | null>(null);
const ROW = "flex w-full items-center gap-3 border-b px-1 py-3 text-left text-[13px] hover:bg-raised/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
</script>

<template>
  <div class="flex flex-col">
  <Loading v-if="memory.isLoading.value" />
  <template v-else>
    <span class="pb-1 pt-4 text-[13px] font-medium">{{ heading }}</span>
    <button type="button" :class="[ROW, 'border-t']" :aria-label="$t('打开 MEMORY.md')" @click="opened = MEMORY_INDEX">
      <span class="min-w-0 flex-1 font-mono underline underline-offset-4">MEMORY.md</span>
      <span class="min-w-0 flex-1 truncate text-muted-foreground max-sm:hidden" :title="label">{{ label }}</span>
      <span class="shrink-0 text-muted-foreground">{{ split.index ? $t("更新于 {updated_at}", { updated_at: timeAgo(split.index.updated_at) }) : $t("还没有内容") }}</span>
    </button>
    <div class="flex items-center justify-between pb-1 pt-6">
      <span class="text-[13px] font-medium">{{ $t("记忆文件") }}</span>
      <el-button size="small" text @click="opened = ''"><Plus />{{ $t("新建") }}</el-button>
    </div>
    <div class="border-t">
      <p v-if="split.files.length === 0" class="border-b px-1 py-3 text-[13px] text-muted-foreground">{{ $t("暂无其他文件。") }}</p>
      <button v-for="f in split.files" :key="f.name" type="button" :class="ROW" :aria-label="$t('打开 {name}', { name: f.name })" @click="opened = f.name">
        <span class="w-44 shrink-0 truncate font-mono max-sm:w-32" :title="f.name">{{ f.name }}</span>
        <span class="min-w-0 flex-1 truncate text-muted-foreground" :title="f.description">{{ f.description }}</span>
        <span class="shrink-0 text-muted-foreground max-sm:hidden">{{ $t("{updated_by} · 更新于 {updated_at}", { updated_by: memoryWriter(f.updated_by), updated_at: timeAgo(f.updated_at) }) }}</span>
      </button>
    </div>
  </template>
  <MemoryFileDialog v-if="opened !== null" :pid="props.pid" :name="opened" @close="opened = null" />
  </div>
</template>
