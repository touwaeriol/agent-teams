<script setup lang="ts">
// One machine: name, online pill, meta, actions (重新检测 / 设置 / 更新 / 重新生成令牌 / 移除), the update line, the slot bar,
// runtime boxes (install and login state as the daemon reported, subscription quota under each) and the sessions running there.
import { $t } from "@/i18n";
import { useQueryClient } from "@tanstack/vue-query";
import { computed, onBeforeUnmount, ref } from "vue";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { Enrollment, NodeRecheck, NodeView } from "@/api/views";
import ProgressBar from "@/components/app/ProgressBar.vue";
import StatusPill from "@/components/app/StatusPill.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Spinner from "@/components/common/Spinner.vue";
import { nodeSlots } from "@/components/shell/nodes";
import { toast } from "@/lib/toast";
import NodeSessions from "./NodeSessions.vue";
import NodeSettings from "./NodeSettings.vue";
import { accountView, cliRows, hoverIntent, needsUsageRefresh, recheckState, refreshCooldown, nodeMeta, runtimeInfos, sharedFolderLine, slotPct, updateView, usageView, withNode, withNodeUsage, type CliTone, type UsageView } from "./nodes";

const props = defineProps<{ node: NodeView }>();
const emit = defineEmits<{ token: [enrollment: Enrollment] }>();
const configuring = ref(false);
const slots = computed(() => nodeSlots(props.node));
const rows = computed(() => cliRows(props.node));
const infos = computed(() => runtimeInfos(props.node));
const usage = computed(() => Object.fromEntries(rows.value.map(({ key: k }) => [k, usageView(k, infos.value[k], props.node.usage?.[k as keyof NonNullable<NodeView["usage"]>], props.node)])) as Record<string, UsageView>);
const accounts = computed(() => Object.fromEntries(rows.value.map(({ key: k }) => [k, accountView(k, infos.value[k], props.node.usage?.[k as keyof NonNullable<NodeView["usage"]>])])));
const CLI_TONE: Record<CliTone, string> = { ok: "hsl(var(--ok))", warn: "hsl(var(--warn))", bad: "hsl(var(--destructive))", muted: "hsl(var(--muted-foreground))" };
const pick = <K extends NonNullable<UsageView>["kind"]>(key: string, kind: K) => {
  const u = usage.value[key];
  return u?.kind === kind ? (u as Extract<NonNullable<UsageView>, { kind: K }>) : undefined;
};
const usageRows = (key: string) => pick(key, "windows")?.rows ?? [];
const usageUpdated = (key: string) => pick(key, "windows")?.updated ?? "";
const usageError = (key: string) => pick(key, "error")?.error ?? "";
// Hovering the runtime boxes for 300 ms asks the node for fresh quota, unless what we have is
// under a minute old or this node was tried in the last minute; one request at a time, failures keep the old data silently.
const queryClient = useQueryClient();
const refreshing = ref(false);
async function refreshUsage() {
  if (refreshing.value || !needsUsageRefresh(props.node) || !refreshCooldown.ready(props.node.id)) return;
  refreshing.value = true;
  refreshCooldown.mark(props.node.id);
  try {
    const { usage: fresh } = await nodeActions.refreshUsage(props.node.id);
    queryClient.setQueryData<NodeView[]>(keys.nodes, (list) => withNodeUsage(list, props.node.id, fresh));
  } catch {
    // keep the old numbers
  } finally {
    refreshing.value = false;
  }
}
const hover = hoverIntent(300, () => void refreshUsage());
// 重新检测: the node checks its CLIs and quota now (up to 15 s); the answer replaces this node in the
// list, then the list is fetched again. The lock is taken synchronously so a double click sends one request.
const recheckable = computed(() => recheckState(props.node));
let rechecking = false;
const recheck = useAction(() => nodeActions.recheck(props.node.id), {
  invalidate: [keys.nodes],
  onSuccess: (r: NodeRecheck) => {
    queryClient.setQueryData<NodeView[]>(keys.nodes, (list) => withNode(list, r.node));
    if (r.stale) toast.warning($t("节点没有及时回应，显示的是上次的检测结果"));
    else toast.success($t("检测完成"));
  },
});
function startRecheck() {
  if (rechecking || !recheckable.value.enabled) return;
  rechecking = true;
  recheck.mutate(undefined, { onSettled: () => void (rechecking = false) });
}
onBeforeUnmount(hover.leave);
const rotate = useAction(() => nodeActions.rotate(props.node.id), { invalidate: [keys.nodes], onSuccess: (e) => emit("token", e) });
const remove = useAction(() => nodeActions.remove(props.node.id), { invalidate: [keys.nodes], success: $t("节点已移除") });
const update = computed(() => updateView(props.node));
const fsLine = computed(() => sharedFolderLine(props.node));
const UPDATE_TONE = { muted: "text-muted-foreground", ok: "text-[hsl(var(--ok))]", warn: "text-[hsl(var(--warn))]", error: "text-destructive" } as const;
const upgrade = useAction((mode: "wait_idle" | "now" | "cancel") => nodeActions.upgrade(props.node.id, mode), { invalidate: [keys.nodes] });
</script>

<template>
  <article class="flex flex-col gap-3.5 rounded-[14px] border bg-panel px-5 py-[18px]">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex flex-wrap items-center gap-2.5">
        <span class="font-mono text-[17px] font-semibold">{{ node.name }}</span>
        <StatusPill :tone="node.online ? '#2E7D4F' : '#8A877F'" :label="node.online ? $t('在线') : $t('离线')" />
        <span class="text-[13px] text-muted-foreground">{{ nodeMeta(node) }}</span>
      </span>
      <span class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
        <el-tooltip :content="recheckable.hint" placement="top" :show-after="200">
          <span class="inline-flex" data-recheck-wrap>
            <el-button size="small" :loading="recheck.isPending.value" :disabled="!recheckable.enabled || recheck.isPending.value" data-recheck @click="startRecheck">
              {{ recheck.isPending.value ? $t("检测中") : $t("重新检测") }}
            </el-button>
          </span>
        </el-tooltip>
        <el-button size="small" @click="configuring = true">{{ $t("设置") }}</el-button>
        <el-dropdown
          v-if="node.online && update.offer"
          split-button
          size="small"
          type="primary"
          :disabled="upgrade.isPending.value"
          data-update
          @click="upgrade.mutate('wait_idle')"
          @command="(m: 'wait_idle' | 'now') => upgrade.mutate(m)"
        >
          {{ $t("更新") }}
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="wait_idle">{{ $t("等正在跑的回合结束后更新") }}</el-dropdown-item>
              <el-dropdown-item command="now">{{ $t("立即更新（中断正在跑的回合）") }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <ConfirmAction :title="$t('重新生成令牌？')" :description="$t('旧令牌立即失效，节点会断开。宿主机用新命令重新安装；容器改 AGENT_TEAMS_NODE_TOKEN 后重建，卷不用动。')" :confirm-label="$t('重新生成')" variant="default" size="small" @confirm="rotate.mutate()">
          {{ $t("重新生成令牌") }}
        </ConfirmAction>
        <ConfirmAction :title="$t('移除节点 {name}？', { name: node.name })" :description="$t('会断开连接；在它上面的员工改派到其他节点继续。')" :confirm-label="$t('移除')" size="small" @confirm="remove.mutate()">{{ $t("移除") }}</ConfirmAction>
      </span>
    </div>
    <div v-if="update.line" class="flex flex-wrap items-center gap-2 text-[13px]" data-update-line>
      <Spinner v-if="update.busy" class="size-3 border" />
      <span :class="UPDATE_TONE[update.tone]">{{ update.line }}</span>
      <el-button v-if="update.canNow" size="small" link type="primary" :disabled="upgrade.isPending.value" @click="upgrade.mutate('now')">{{ $t("立即更新") }}</el-button>
      <el-button v-if="update.canCancel" size="small" link :disabled="upgrade.isPending.value" @click="upgrade.mutate('cancel')">{{ $t("取消") }}</el-button>
    </div>
    <div v-if="fsLine" class="text-[13px]" data-fs-line>
      <el-tooltip v-if="fsLine.error" :content="fsLine.error" placement="bottom-start" :show-after="200">
        <span class="cursor-help underline decoration-dotted" :class="UPDATE_TONE[fsLine.tone]">{{ fsLine.text }}</span>
      </el-tooltip>
      <span v-else :class="UPDATE_TONE[fsLine.tone]">{{ fsLine.text }}</span>
    </div>
    <div class="flex flex-col gap-1.5">
      <span class="flex justify-between text-xs text-muted-foreground">
        <span>{{ $t("槽位（同时运行的会话）") }}</span>
        <span>{{ node.active_sessions }} / {{ slots || "—" }}{{ node.queued ? $t(" · 排队 {queued}", { queued: node.queued }) : "" }}</span>
      </span>
      <span class="flex"><ProgressBar :pct="slotPct(node)" tone="#2F6FDB" :height="6" /></span>
    </div>
    <div class="flex flex-wrap gap-2 text-[13px]" @mouseenter="hover.enter" @mouseleave="hover.leave">
      <div v-for="row in rows" :key="row.key" class="flex min-w-[260px] flex-1 basis-0 flex-col gap-1.5 rounded-lg border border-[#EFEDE8] bg-background px-2.5 dark:border-border py-1.5" :data-runtime="row.key" :data-tone="row.tone">
        <span class="flex flex-wrap items-center gap-x-2">
          <span class="font-medium">{{ row.label }}</span>
          <span class="font-mono text-xs text-[#4A4843] dark:text-foreground/70" data-ver>{{ row.ver }}</span>
          <el-tooltip v-if="row.error" :content="row.error" placement="bottom-start" :show-after="200">
            <span class="cursor-help text-xs underline decoration-dotted" :style="{ color: CLI_TONE[row.tone] }" data-login>{{ row.login }}</span>
          </el-tooltip>
          <span v-else class="text-xs" :style="{ color: CLI_TONE[row.tone] }" data-login>{{ row.login }}</span>
          <span v-if="recheck.isPending.value" class="ml-auto flex items-center gap-1 text-[11px] text-muted-foreground" data-checking><Spinner class="size-2.5 border" />{{ $t("检测中…") }}</span>
          <el-tooltip v-else-if="row.checked" :content="row.checkedTitle" placement="top" :show-after="200">
            <span class="ml-auto cursor-default text-[11px] text-muted-foreground" data-checked>{{ row.checked }}</span>
          </el-tooltip>
        </span>
        <div v-if="accounts[row.key]" class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs" data-account>
          <span v-if="accounts[row.key]!.email" class="min-w-0 break-all text-[#4A4843] dark:text-foreground/70" data-email>{{ accounts[row.key]!.email }}</span>
          <span v-if="accounts[row.key]!.plan" class="shrink-0 rounded border px-1.5 text-[11px] leading-[18px] text-muted-foreground" data-plan>{{ accounts[row.key]!.plan }}</span>
        </div>
        <template v-if="usage[row.key]?.kind === 'windows'">
          <div class="grid grid-cols-[auto_minmax(40px,1fr)_auto_auto] items-center gap-x-2 gap-y-1 text-xs">
            <template v-for="(win, i) in usageRows(row.key)" :key="`${win.key}|${win.label}|${i}`">
              <span class="whitespace-nowrap text-muted-foreground">{{ win.label }}</span>
              <ProgressBar :pct="win.width" :tone="win.tone" :height="5" />
              <span class="text-right font-mono tabular-nums" :style="{ color: win.pct >= 80 ? win.tone : undefined }">{{ win.pct }}%</span>
              <span class="whitespace-nowrap text-[11px] text-muted-foreground">{{ win.reset }}</span>
            </template>
          </div>
          <span v-if="refreshing" class="flex items-center gap-1 text-[11px] text-muted-foreground" data-refreshing><Spinner class="size-2.5 border" />{{ $t("刷新中…") }}</span>
          <span v-else class="text-[11px] text-muted-foreground">{{ usageUpdated(row.key) }}</span>
        </template>
        <el-tooltip v-else-if="usage[row.key]?.kind === 'error'" :content="usageError(row.key)" placement="bottom-start" :show-after="200">
          <span class="flex w-fit cursor-help items-center gap-1.5 text-xs text-destructive">{{ $t("获取失败") }}<Spinner v-if="refreshing" class="size-2.5 border text-muted-foreground" /></span>
        </el-tooltip>
        <span v-else-if="usage[row.key]?.kind === 'pending'" class="text-[11px] text-muted-foreground">{{ $t("余量获取中…") }}</span>
      </div>
    </div>
    <NodeSessions v-if="node.online" :node-id="node.id" />
    <NodeSettings v-model:open="configuring" :node="node" />
  </article>
</template>
