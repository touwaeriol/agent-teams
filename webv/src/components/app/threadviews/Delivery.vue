<script setup lang="ts">
import { computed } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useChecks, useMergeQueue } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import { safeHref } from "@/lib/safeurl";
import StateMark from "../StateMark.vue";
import { checkLook, openPrMode, prLines, queueLines } from "./delivery";
import DeliveryRow from "./DeliveryRow.vue";

const props = defineProps<{ thread: ThreadView }>();
const queue = useMergeQueue(() => props.thread.id);
const checks = useChecks(() => props.thread.id);
const openPr = useAction((force: boolean) => threadActions.openPr(props.thread.id, force), {
  invalidate: () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id)],
  success: "PR 已开，讨论串进入待验收",
});
const mode = computed(() => openPrMode(props.thread));
const lines = computed(() => [...queueLines(props.thread, queue.data.value ?? []), ...prLines(props.thread)]);
const checkRows = computed(() => (checks.data.value ?? []).map((c) => ({ c, look: checkLook(c) })));
</script>

<template>
  <section class="flex flex-col gap-2">
    <span class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
      代码
      <el-button v-if="mode === 'open'" type="primary" size="small" :disabled="openPr.isPending.value" @click="openPr.mutate(false)">开 PR</el-button>
      <ConfirmAction
        v-else-if="mode === 'confirm'"
        title="主要任务还没全部完成"
        description="现在开 PR 会跳过“主要任务全部完成”这一条检查，组长剩下的工作会继续推到同一个分支。"
        confirm-label="仍然开 PR"
        variant="primary"
        size="small"
        @confirm="openPr.mutate(true)"
      >
        开 PR
      </ConfirmAction>
    </span>
    <DeliveryRow v-for="l in lines" :key="l.key" :branch="l.branch" :what="l.what" :look="l.look" :href="l.href" />
    <a v-for="{ c, look } in checkRows" :key="c.name" :href="safeHref(c.url)" target="_blank" rel="noreferrer" class="flex items-center gap-2 px-2.5 text-[13px]">
      <StateMark :look="look" />
      <span class="font-mono">{{ c.name }}</span>
      <span :style="{ color: look.tone }">{{ look.label }}</span>
      <span class="truncate text-muted-foreground">{{ c.summary }}</span>
    </a>
  </section>
</template>
