<script setup lang="ts">
// 记忆: 项目指令 (written by you) and 自动记忆 (MEMORY.md + topic files the employees keep with the
// memory_* tools). Every new session reads the instructions and MEMORY.md; files open in a dialog
// to read, edit or delete.
import { Plus } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useMemory } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import { timeAgo } from "@/lib/utils";
import Divider from "./Divider.vue";
import Heading from "./Heading.vue";
import InstructionsField from "./InstructionsField.vue";
import { MEMORY_INDEX, memoryWriter, splitMemory } from "./logic";
import MemoryFileDialog from "./MemoryFileDialog.vue";

const props = defineProps<{ project: ProjectView }>();
const memory = useMemory(() => props.project.id);
const split = computed(() => splitMemory(memory.data.value));
/** The open dialog: a file name, "" for a new file, null when closed. */
const opened = ref<string | null>(null);
const ROW = "flex w-full items-center gap-3 border-b px-1 py-3 text-left text-[13px] hover:bg-raised/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
</script>

<template>
  <div class="flex flex-col">
    <Heading>记忆</Heading>
    <InstructionsField :project="project" />
    <Divider />
    <div class="flex flex-col gap-0.5 pt-2">
      <h3 class="font-medium">自动记忆</h3>
      <p class="text-[13px] leading-5 text-muted-foreground">Claude 在此项目中工作时自行记录的笔记：做过的决定、约定、你的偏好、踩过的坑。每个新会话开始时读取 MEMORY.md，需要时再读各个记忆文件。</p>
    </div>
    <Loading v-if="memory.isLoading.value" />
    <template v-else>
      <span class="pb-1 pt-4 text-[13px] font-medium">阅读所有讨论串</span>
      <button type="button" :class="[ROW, 'border-t']" aria-label="打开 MEMORY.md" @click="opened = MEMORY_INDEX">
        <span class="min-w-0 flex-1 font-mono underline underline-offset-4">MEMORY.md</span>
        <span class="min-w-0 flex-1 truncate text-muted-foreground max-sm:hidden">{{ project.name }} 项目</span>
        <span class="shrink-0 text-muted-foreground">{{ split.index ? `更新于 ${timeAgo(split.index.updated_at)}` : "还没有内容" }}</span>
      </button>
      <div class="flex items-center justify-between pb-1 pt-6">
        <span class="text-[13px] font-medium">记忆文件</span>
        <el-button size="small" text @click="opened = ''"><Plus />新建</el-button>
      </div>
      <div class="border-t">
        <p v-if="split.files.length === 0" class="border-b px-1 py-3 text-[13px] text-muted-foreground">暂无其他文件。</p>
        <button v-for="f in split.files" :key="f.name" type="button" :class="ROW" :aria-label="`打开 ${f.name}`" @click="opened = f.name">
          <span class="w-44 shrink-0 truncate font-mono max-sm:w-32">{{ f.name }}</span>
          <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ f.description }}</span>
          <span class="shrink-0 text-muted-foreground max-sm:hidden">{{ memoryWriter(f.updated_by) }} · 更新于 {{ timeAgo(f.updated_at) }}</span>
        </button>
      </div>
    </template>
    <MemoryFileDialog v-if="opened !== null" :pid="project.id" :name="opened" @close="opened = null" />
  </div>
</template>
