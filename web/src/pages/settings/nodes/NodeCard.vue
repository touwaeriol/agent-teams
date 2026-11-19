<script setup lang="ts">
// One machine: name, online pill, meta, actions (设置 / 升级 / 重新生成令牌 / 移除), the slot bar,
// runtime chips (with subscription quota under each) and the sessions running there.
import { computed, ref } from "vue";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { Enrollment, NodeView } from "@/api/views";
import ProgressBar from "@/components/app/ProgressBar.vue";
import StatusPill from "@/components/app/StatusPill.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import { nodeSlots } from "@/components/shell/nodes";
import NodeSessions from "./NodeSessions.vue";
import NodeSettings from "./NodeSettings.vue";
import { nodeMeta, RUNTIMES, runtimeLine, slotPct, usageView, type RuntimeInfo, type UsageView } from "./nodes";

const props = defineProps<{ node: NodeView }>();
const emit = defineEmits<{ token: [enrollment: Enrollment] }>();
const configuring = ref(false);
const slots = computed(() => nodeSlots(props.node));
const runtimes = computed(() => (props.node.runtimes ?? {}) as Record<string, RuntimeInfo>);
const usage = computed(() => Object.fromEntries(RUNTIMES.map(([k]) => [k, usageView(k, runtimes.value[k], props.node.usage?.[k as "claude" | "codex"], props.node.online)])) as Record<string, UsageView>);
const pick = <K extends NonNullable<UsageView>["kind"]>(key: string, kind: K) => {
  const u = usage.value[key];
  return u?.kind === kind ? (u as Extract<NonNullable<UsageView>, { kind: K }>) : undefined;
};
const usageRows = (key: string) => pick(key, "windows")?.rows ?? [];
const usageUpdated = (key: string) => pick(key, "windows")?.updated ?? "";
const usageError = (key: string) => pick(key, "error")?.error ?? "";
const rotate = useAction(() => nodeActions.rotate(props.node.id), { invalidate: [keys.nodes], onSuccess: (e) => emit("token", e) });
const remove = useAction(() => nodeActions.remove(props.node.id), { invalidate: [keys.nodes], success: "节点已移除" });
const upgrade = useAction(() => nodeActions.upgrade(props.node.id), { success: "已让节点升级" });
</script>

<template>
  <article class="flex flex-col gap-3.5 rounded-[14px] border bg-panel px-5 py-[18px]">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex flex-wrap items-center gap-2.5">
        <span class="font-mono text-[17px] font-semibold">{{ node.name }}</span>
        <StatusPill :tone="node.online ? '#2E7D4F' : '#8A877F'" :label="node.online ? '在线' : '离线'" />
        <span class="text-[13px] text-muted-foreground">{{ nodeMeta(node) }}</span>
      </span>
      <span class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
        <el-button size="small" @click="configuring = true">设置</el-button>
        <el-button v-if="node.online" size="small" :disabled="upgrade.isPending.value" @click="upgrade.mutate()">升级</el-button>
        <ConfirmAction title="重新生成令牌？" description="旧令牌立即失效，节点会断开，需要用新命令重新安装。" confirm-label="重新生成" variant="default" size="small" @confirm="rotate.mutate()">
          重新生成令牌
        </ConfirmAction>
        <ConfirmAction :title="`移除节点 ${node.name}？`" description="会断开连接；在它上面的员工改派到其他节点继续。" confirm-label="移除" size="small" @confirm="remove.mutate()">移除</ConfirmAction>
      </span>
    </div>
    <div class="flex flex-col gap-1.5">
      <span class="flex justify-between text-xs text-muted-foreground">
        <span>槽位（同时运行的会话）</span>
        <span>{{ node.active_sessions }} / {{ slots || "—" }}{{ node.queued ? ` · 排队 ${node.queued}` : "" }}</span>
      </span>
      <ProgressBar :pct="slotPct(node)" tone="#2F6FDB" :height="6" />
    </div>
    <div class="flex flex-wrap gap-2 text-[13px]">
      <div v-for="[key, label] in RUNTIMES" :key="key" class="flex min-w-[260px] flex-1 basis-0 flex-col gap-1.5 rounded-lg border border-[#EFEDE8] bg-background px-2.5 py-1.5" :data-runtime="key">
        <span class="flex flex-wrap items-center gap-x-2">
          <span class="font-medium">{{ label }}</span>
          <span class="font-mono text-xs text-[#4A4843]">{{ runtimeLine(key, runtimes[key]).ver }}</span>
          <span class="text-xs" :style="{ color: runtimeLine(key, runtimes[key]).ok ? '#2E7D4F' : '#B4530B' }">{{ runtimeLine(key, runtimes[key]).login }}</span>
        </span>
        <template v-if="usage[key]?.kind === 'windows'">
          <div class="grid grid-cols-[auto_minmax(40px,1fr)_auto_auto] items-center gap-x-2 gap-y-1 text-xs">
            <template v-for="row in usageRows(key)" :key="row.key">
              <span class="whitespace-nowrap text-muted-foreground">{{ row.label }}</span>
              <ProgressBar :pct="row.width" :tone="row.tone" :height="5" />
              <span class="text-right font-mono tabular-nums" :style="{ color: row.pct >= 80 ? row.tone : undefined }">{{ row.pct }}%</span>
              <span class="whitespace-nowrap text-[11px] text-muted-foreground">{{ row.reset }}</span>
            </template>
          </div>
          <span class="text-[11px] text-muted-foreground">{{ usageUpdated(key) }}</span>
        </template>
        <el-tooltip v-else-if="usage[key]?.kind === 'error'" :content="usageError(key)" placement="bottom-start" :show-after="200">
          <span class="w-fit cursor-help text-xs text-destructive">获取失败</span>
        </el-tooltip>
        <span v-else-if="usage[key]?.kind === 'pending'" class="text-[11px] text-muted-foreground">余量获取中…</span>
      </div>
    </div>
    <NodeSessions v-if="node.online" :node-id="node.id" />
    <NodeSettings v-model:open="configuring" :node="node" />
  </article>
</template>
