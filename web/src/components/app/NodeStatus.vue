<script setup lang="ts">
import { $t } from "@/i18n";
/** The nodes a thread's employees run on, as one monitor icon: green all online, orange some offline, red all offline, grey none; names in the tooltip. */
import { Monitor, MonitorOff } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { INK } from "@/lib/present";
import { threadNodes, type NodeState } from "@/lib/threadList";

const props = withDefaults(defineProps<{ thread: Pick<ThreadView, "members">; size?: number }>(), { size: 15 });
const info = computed(() => threadNodes(props.thread));
const TONE: Record<NodeState, string> = { online: INK.done, partial: INK.wait, offline: INK.bad, none: INK.idle };
</script>

<template>
  <el-tooltip :content="$t('节点：{label}', { label: info.label })" placement="bottom" :show-after="200" :hide-after="0" effect="dark" :show-arrow="false">
    <span class="relative inline-flex shrink-0 items-center justify-center text-muted-foreground" role="img" :aria-label="$t('节点：{label}', { label: info.label })" :data-node="info.state" tabindex="-1">
      <MonitorOff v-if="info.state === 'offline'" :size="size" :stroke-width="1.8" />
      <Monitor v-else :size="size" :stroke-width="1.8" />
      <span class="absolute -bottom-px -right-0.5 size-[7px] rounded-full ring-[1.5px] ring-panel" :style="{ background: TONE[info.state] }" />
    </span>
  </el-tooltip>
</template>
