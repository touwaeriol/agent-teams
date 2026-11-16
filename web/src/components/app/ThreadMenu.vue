<script setup lang="ts">
// The ⋯ menu of a thread: (任务 / 交付物 / 成员 with `view` listeners), 全屏打开, 暂停 / 继续,
// 归档 (asks first while it is still going) / 取消归档, 删除 (asks first).
import { Archive, ArchiveRestore, ListChecks, Maximize2, MoreHorizontal, Package, Pause, Play, Trash2, Users } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import IconMenu from "@/components/common/IconMenu.vue";
import { threadActive } from "@/lib/live";
import { archiveNotice, menuItems, THREAD_VIEWS, type ThreadViewId } from "./threadMenu";

const props = defineProps<{ thread: ThreadView; fullHref?: string; withViews?: boolean; class?: string }>();
const emit = defineEmits<{ deleted: []; view: [view: ThreadViewId] }>();
const router = useRouter();
const confirm = ref<"archive" | "delete" | null>(null);
const invalidate = () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)];
const update = useAction((status: "paused" | "running") => threadActions.update(props.thread.id, { status }), { invalidate });
const archive = useAction(() => threadActions.archive(props.thread.id), { invalidate, success: `#${props.thread.no} 已归档` });
const unarchive = useAction(() => threadActions.unarchive(props.thread.id), { invalidate, success: `#${props.thread.no} 已取消归档` });
const remove = useAction(() => threadActions.remove(props.thread.id), { invalidate, success: `#${props.thread.no} 已删除`, onSuccess: () => emit("deleted") });
const items = computed(() => menuItems(props.thread));
const notice = computed(() => archiveNotice(props.thread.no));
const VIEW_ICON = { tasks: ListChecks, deliverables: Package, members: Users } as const;

const COMMANDS: Record<string, () => void> = {
  full: () => props.fullHref && void router.push(props.fullHref),
  pause: () => update.mutate("paused"),
  resume: () => update.mutate("running"),
  archive: () => (threadActive(props.thread) ? (confirm.value = "archive") : archive.mutate()),
  unarchive: () => unarchive.mutate(),
  delete: () => (confirm.value = "delete"),
};
function run(command: string) {
  if (command.startsWith("view:")) return emit("view", command.slice(5) as ThreadViewId);
  COMMANDS[command]?.();
}
const setConfirm = (kind: "archive" | "delete", open: boolean) => (confirm.value = open ? kind : null);
</script>

<template>
  <IconMenu :label="`讨论串 #${thread.no} 的操作`" :class="props.class" @command="run">
    <MoreHorizontal :stroke-width="1.8" />
    <template #menu>
        <template v-if="withViews">
          <el-dropdown-item v-for="[id, label] in THREAD_VIEWS" :key="id" :command="`view:${id}`"><component :is="VIEW_ICON[id]" /> {{ label }}</el-dropdown-item>
        </template>
        <el-dropdown-item v-if="fullHref" command="full" :divided="withViews"><Maximize2 /> 全屏打开</el-dropdown-item>
        <el-dropdown-item v-if="items.pause" command="pause"><Pause /> 暂停</el-dropdown-item>
        <el-dropdown-item v-if="items.resume" command="resume"><Play /> 继续</el-dropdown-item>
        <el-dropdown-item v-if="items.archive" command="archive"><Archive /> 归档</el-dropdown-item>
        <el-dropdown-item v-if="items.unarchive" command="unarchive"><ArchiveRestore /> 取消归档</el-dropdown-item>
      <el-dropdown-item command="delete" divided class="is-destructive"><Trash2 /> 删除</el-dropdown-item>
    </template>
  </IconMenu>
  <ConfirmAction
    :open="confirm === 'archive'"
    :title="notice.title"
    :description="notice.description"
    confirm-label="归档"
    variant="default"
    @update:open="(o) => setConfirm('archive', o)"
    @confirm="archive.mutate()"
  />
  <ConfirmAction
    :open="confirm === 'delete'"
    :title="`删除 #${thread.no} ${thread.title}？`"
    description="会关掉员工的会话、取消没做完的任务、删掉节点上的工作目录（包括未推送的改动）和成员分支，对话、任务和审批记录一并删除，不能恢复。它做的交付物会留在资料库里。"
    confirm-label="删除"
    @update:open="(o) => setConfirm('delete', o)"
    @confirm="remove.mutate()"
  />
</template>
