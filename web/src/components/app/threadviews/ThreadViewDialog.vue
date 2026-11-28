<script setup lang="ts">
// The thread's side views, opened from the ⋯ menu over the chat: 任务 (main tasks with their
// small tasks), 交付物 (PRs, merge queue, CI and published deliverables) and 成员 (who is in
// the thread, where they run, stop buttons). The chat itself stays conversation only.
import { $t } from "@/i18n";
import { X } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { cn } from "@/lib/utils";
import { THREAD_VIEWS, type ThreadViewId } from "../threadMenu";
import DeliverablesView from "./DeliverablesView.vue";
import MembersView from "./MembersView.vue";
import TasksView from "./TasksView.vue";

const props = defineProps<{ pid: string; thread: ThreadView; view: ThreadViewId | null }>();
const emit = defineEmits<{ view: [view: ThreadViewId | null] }>();
const open = computed({ get: () => props.view !== null, set: (o: boolean) => !o && emit("view", null) });
const tab = (id: ThreadViewId) => cn("h-9 border-b-2 px-2 text-[13px]", props.view === id ? "border-foreground font-semibold" : "border-transparent text-muted-foreground hover:text-foreground");
</script>

<template>
  <el-dialog v-model="open" align-center append-to-body :show-close="false" class="at-view-dialog" width="min(640px, calc(100vw - 32px))" :aria-label="`#${thread.no} ${thread.title}`">
    <div class="flex max-h-[85vh] flex-col">
      <div class="flex shrink-0 flex-col gap-2 border-b px-5 pb-0 pt-4">
        <h2 class="truncate pr-8 text-[15px] font-semibold">#{{ thread.no }} {{ thread.title }}</h2>
        <nav :aria-label="$t('讨论串视图')" class="-mb-px flex gap-1">
          <button v-for="[id, label] in THREAD_VIEWS" :key="id" type="button" :aria-pressed="view === id" :class="tab(id)" @click="emit('view', id)">{{ label }}</button>
        </nav>
      </div>
      <div class="scroll-thin flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
        <TasksView v-if="view === 'tasks'" :thread="thread" />
        <DeliverablesView v-else-if="view === 'deliverables'" :pid="pid" :thread="thread" />
        <MembersView v-else-if="view === 'members'" :thread="thread" />
      </div>
    </div>
    <button type="button" class="absolute right-4 top-4 rounded-md p-1 text-muted-foreground hover:bg-raised hover:text-foreground" @click="open = false">
      <X class="size-4" />
      <span class="sr-only">{{ $t("关闭") }}</span>
    </button>
  </el-dialog>
</template>

<style>
/* Flush dialog: the header, tabs and body draw their own padding (React DialogContent p-0 gap-0). */
.el-dialog.at-view-dialog { padding: 0; max-width: none; position: relative; }
.el-dialog.at-view-dialog > .el-dialog__header { display: none; }
.el-dialog.at-view-dialog > .el-dialog__body { padding: 0; }
</style>
