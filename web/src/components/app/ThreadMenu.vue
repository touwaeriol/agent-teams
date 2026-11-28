<script setup lang="ts">
// The ⋯ menu of a thread, laid out like the Claude app's (icons left, shortcuts right in grey):
//   上一个 / 下一个讨论串 (Alt+↑ / Alt+↓; the header only, with `nav`)
//   在新窗口中打开 · 全屏打开 (with `fullHref`) · 复制链接
//   任务 / 交付物 / 成员 (with `withViews`)
//   重命名 · 置顶 / 取消置顶 · 添加提醒 ▸ (1 小时后 / 今天晚些时候 / 明天上午 9 点 / 下周一 9 点 / 自定义… / 取消提醒)
//   解决 (标记为完成, asks first; Ctrl+Alt+Enter) or 重新打开 · 暂停 / 继续 · 归档 / 取消归档
//   发送反馈 · 删除 (asks first)
// With `shortcuts` (the open thread's header) the keys work while the thread is shown.
import { $t } from "@/i18n";
import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  BellOff,
  BellPlus,
  ChevronRight,
  CircleCheck,
  ExternalLink,
  Link,
  ListChecks,
  Maximize2,
  MessageSquareWarning,
  MoreHorizontal,
  Package,
  Pause,
  Pencil,
  Pin,
  PinOff,
  Play,
  RotateCcw,
  Trash2,
  Users,
} from "lucide-vue-next";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import IconMenu from "@/components/common/IconMenu.vue";
import { threadActive } from "@/lib/live";
import { askNotificationPermission } from "@/lib/reminders";
import { keyLabel, threadShortcut } from "@/lib/shortcuts";
import { reminderLabel, reminderPresets } from "@/lib/threadList";
import { toast } from "@/lib/toast";
import FeedbackDialog from "./thread/FeedbackDialog.vue";
import ReminderDialog from "./thread/ReminderDialog.vue";
import { archiveNotice, completeNotice, menuItems, THREAD_VIEWS, threadFullPath, threadLink, type ThreadViewId } from "./threadMenu";

const props = defineProps<{
  thread: ThreadView;
  fullHref?: string;
  withViews?: boolean;
  /** Routes of the threads before and after this one in the list (null at an end). */
  nav?: { prev: string | null; next: string | null };
  /** Listen for Alt+↑ / Alt+↓ / Ctrl+Alt+Enter (the open thread's header). */
  shortcuts?: boolean;
  class?: string;
}>();
const emit = defineEmits<{ deleted: []; view: [view: ThreadViewId]; rename: [] }>();
const router = useRouter();
const menu = ref<InstanceType<typeof IconMenu> | null>(null);
const confirm = ref<"complete" | "archive" | "delete" | null>(null);
const customReminder = ref(false);
const feedback = ref(false);
const invalidate = () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)];
const no = computed(() => props.thread.no);
const update = useAction((status: "paused" | "running") => threadActions.update(props.thread.id, { status }), { invalidate });
const pin = useAction((pinned: boolean) => threadActions.update(props.thread.id, { pinned }), {
  invalidate,
  success: (t) => (t.pinned_at ? $t("#{no} 已置顶", { no: no.value }) : $t("#{no} 已取消置顶", { no: no.value })),
});
const remind = useAction((at: Date) => threadActions.remind(props.thread.id, at), {
  invalidate,
  success: (t) => $t("会在 {reminderLabel} 提醒你 #{no}", { reminderLabel: reminderLabel(t.remind_at ?? ""), no: no.value }),
});
const clearReminder = useAction(() => threadActions.clearReminder(props.thread.id), { invalidate, success: $t("已取消 #{no} 的提醒", { no: props.thread.no }) });
const archive = useAction(() => threadActions.archive(props.thread.id), { invalidate, success: $t("#{no} 已归档", { no: props.thread.no }) });
const complete = useAction(() => threadActions.complete(props.thread.id), { invalidate, success: $t("#{no} 已标记为完成", { no: props.thread.no }) });
const reopen = useAction(() => threadActions.reopen(props.thread.id), { invalidate, success: $t("#{no} 已重新打开", { no: props.thread.no }) });
const unarchive = useAction(() => threadActions.unarchive(props.thread.id), { invalidate, success: $t("#{no} 已取消归档", { no: props.thread.no }) });
const remove = useAction(() => threadActions.remove(props.thread.id), { invalidate, success: $t("#{no} 已删除", { no: props.thread.no }), onSuccess: () => emit("deleted") });
const items = computed(() => menuItems(props.thread));
const notice = computed(() => archiveNotice(props.thread.no));
const doneNotice = computed(() => completeNotice(props.thread.no));
/** The submenu's times, from when it was last opened. */
const now = ref(new Date());
const presets = computed(() => reminderPresets(now.value));
const VIEW_ICON = { tasks: ListChecks, deliverables: Package, members: Users } as const;
const KEYS = { prev: keyLabel("Alt+↑"), next: keyLabel("Alt+↓"), resolve: keyLabel("Ctrl+Alt+Enter") };

async function copyLink() {
  try {
    await navigator.clipboard.writeText(threadLink(props.thread));
    toast.success($t("已复制链接"));
  } catch {
    toast.error($t("复制失败，请从地址栏复制"));
  }
}

function setReminder(at: Date) {
  void askNotificationPermission();
  remind.mutate(at);
}

const go = (path: string | null | undefined) => path && void router.push(path);

function resolve() {
  if (items.value.complete) confirm.value = "complete";
  else if (items.value.reopen) reopen.mutate();
}

const COMMANDS: Record<string, () => void> = {
  prev: () => go(props.nav?.prev),
  next: () => go(props.nav?.next),
  window: () => window.open(threadFullPath(props.thread), "_blank", "noopener"),
  full: () => go(props.fullHref),
  copy: () => void copyLink(),
  rename: () => emit("rename"),
  pin: () => pin.mutate(true),
  unpin: () => pin.mutate(false),
  complete: () => (confirm.value = "complete"),
  reopen: () => reopen.mutate(),
  pause: () => update.mutate("paused"),
  resume: () => update.mutate("running"),
  archive: () => (threadActive(props.thread) ? (confirm.value = "archive") : archive.mutate()),
  unarchive: () => unarchive.mutate(),
  feedback: () => (feedback.value = true),
  delete: () => (confirm.value = "delete"),
};
function run(command: string) {
  if (command.startsWith("view:")) return emit("view", command.slice(5) as ThreadViewId);
  COMMANDS[command]?.();
}

/** A pick in the 添加提醒 submenu; the parent menu closes too. */
function pickReminder(command: string) {
  menu.value?.close();
  if (command === "custom") return void (customReminder.value = true);
  if (command === "clear") return clearReminder.mutate();
  const preset = reminderPresets(new Date()).find((p) => p.id === command);
  if (preset) setReminder(preset.at);
}

const setConfirm = (kind: "complete" | "archive" | "delete", open: boolean) => (confirm.value = open ? kind : null);

const SHORTCUT: Record<string, () => void> = { prev: () => go(props.nav?.prev), next: () => go(props.nav?.next), resolve };
function onKey(e: KeyboardEvent) {
  const which = threadShortcut(e);
  if (!which || confirm.value) return;
  e.preventDefault();
  e.stopPropagation();
  SHORTCUT[which]();
}
onMounted(() => props.shortcuts && window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

defineExpose({ resolve });
</script>

<template>
  <IconMenu ref="menu" :label="$t('讨论串 #{no} 的操作', { no: thread.no })" :class="props.class" popper-class="w-64" @command="run">
    <MoreHorizontal :stroke-width="1.8" />
    <template #menu>
      <template v-if="nav">
        <el-dropdown-item command="prev" :disabled="!nav.prev"><ArrowUp /> {{ $t("上一个讨论串") }}<kbd class="at-kbd">{{ KEYS.prev }}</kbd></el-dropdown-item>
        <el-dropdown-item command="next" :disabled="!nav.next"><ArrowDown /> {{ $t("下一个讨论串") }}<kbd class="at-kbd">{{ KEYS.next }}</kbd></el-dropdown-item>
      </template>
      <el-dropdown-item command="window" :divided="Boolean(nav)"><ExternalLink /> {{ $t("在新窗口中打开") }}</el-dropdown-item>
      <el-dropdown-item v-if="fullHref" command="full"><Maximize2 /> {{ $t("全屏打开") }}</el-dropdown-item>
      <el-dropdown-item command="copy"><Link /> {{ $t("复制链接") }}</el-dropdown-item>
      <template v-if="withViews">
        <el-dropdown-item v-for="([id, label], i) in THREAD_VIEWS" :key="id" :command="`view:${id}`" :divided="i === 0"><component :is="VIEW_ICON[id]" /> {{ label }}</el-dropdown-item>
      </template>
      <el-dropdown-item command="rename" divided><Pencil /> {{ $t("重命名") }}</el-dropdown-item>
      <el-dropdown-item v-if="items.pin" command="pin"><Pin /> {{ $t("置顶") }}</el-dropdown-item>
      <el-dropdown-item v-if="items.unpin" command="unpin"><PinOff /> {{ $t("取消置顶") }}</el-dropdown-item>
      <li v-if="items.remind" role="none" class="at-submenu">
        <el-dropdown trigger="hover" placement="left-start" popper-class="at-menu w-60" :show-arrow="false" :show-timeout="80" @command="pickReminder" @visible-change="(v: boolean) => v && (now = new Date())">
          <span role="menuitem" tabindex="-1" class="el-dropdown-menu__item w-full" data-submenu="reminder">
            <BellPlus /> {{ thread.remind_at ? $t("修改提醒") : $t("添加提醒") }}
            <span v-if="thread.remind_at" class="at-kbd">{{ reminderLabel(thread.remind_at) }}</span>
            <ChevronRight :class="thread.remind_at ? '!ml-1' : 'ml-auto'" class="text-muted-foreground" />
          </span>
          <template #dropdown>
            <el-dropdown-menu :aria-label="$t('添加提醒')">
              <el-dropdown-item v-for="p in presets" :key="p.id" :command="p.id">{{ p.label }}<span class="at-kbd">{{ reminderLabel(p.at.toISOString()) }}</span></el-dropdown-item>
              <el-dropdown-item command="custom" divided>{{ $t("自定义…") }}</el-dropdown-item>
              <el-dropdown-item v-if="items.clearReminder" command="clear" divided><BellOff /> {{ thread.remind_at ? $t("取消提醒") : $t("清除提醒") }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </li>
      <el-dropdown-item v-if="items.complete" command="complete" divided><CircleCheck /> {{ $t("解决") }}<kbd class="at-kbd">{{ KEYS.resolve }}</kbd></el-dropdown-item>
      <el-dropdown-item v-if="items.reopen" command="reopen" divided><RotateCcw /> {{ $t("重新打开") }}<kbd class="at-kbd">{{ KEYS.resolve }}</kbd></el-dropdown-item>
      <el-dropdown-item v-if="items.pause" command="pause" :divided="!items.complete && !items.reopen"><Pause /> {{ $t("暂停") }}</el-dropdown-item>
      <el-dropdown-item v-if="items.resume" command="resume"><Play /> {{ $t("继续") }}</el-dropdown-item>
      <el-dropdown-item v-if="items.archive" command="archive" :divided="!items.complete && !items.reopen && !items.pause"><Archive /> {{ $t("归档") }}</el-dropdown-item>
      <el-dropdown-item v-if="items.unarchive" command="unarchive" divided><ArchiveRestore /> {{ $t("取消归档") }}</el-dropdown-item>
      <el-dropdown-item command="feedback" divided><MessageSquareWarning /> {{ $t("发送反馈") }}</el-dropdown-item>
      <el-dropdown-item command="delete" class="is-destructive"><Trash2 /> {{ $t("删除") }}</el-dropdown-item>
    </template>
  </IconMenu>
  <ConfirmAction
    :open="confirm === 'complete'"
    :title="doneNotice.title"
    :description="doneNotice.description"
    :confirm-label="$t('标记为完成')"
    variant="default"
    @update:open="(o) => setConfirm('complete', o)"
    @confirm="complete.mutate()"
  />
  <ConfirmAction
    :open="confirm === 'archive'"
    :title="notice.title"
    :description="notice.description"
    :confirm-label="$t('归档')"
    variant="default"
    @update:open="(o) => setConfirm('archive', o)"
    @confirm="archive.mutate()"
  />
  <ConfirmAction
    :open="confirm === 'delete'"
    :title="$t('删除 #{no} {title}？', { no: thread.no, title: thread.title })"
    :description="$t('会关掉员工的会话、取消没做完的任务、删掉节点上的工作目录（包括未推送的改动）和成员分支，对话、任务和审批记录一并删除，不能恢复。它做的交付物会留在资料库里。')"
    :confirm-label="$t('删除')"
    @update:open="(o) => setConfirm('delete', o)"
    @confirm="remove.mutate()"
  />
  <ReminderDialog v-if="customReminder" v-model:open="customReminder" :thread="thread" @pick="setReminder" />
  <FeedbackDialog v-if="feedback" v-model:open="feedback" :thread="thread" />
</template>
