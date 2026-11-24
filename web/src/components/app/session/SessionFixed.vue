<script setup lang="ts">
/**
 * What a session was started with and can no longer change (#53): node, role and agent (CLI),
 * read-only at the start of the box's controls row (model, effort and mode follow and can still
 * be switched). Narrow screens show the agent only; the title has all three.
 */
import { Lock } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { useIsMobile } from "@/lib/media";
import { leadOf } from "@/lib/present";
import { runtimeLabel } from "@/lib/runtimes";
import { fixedParts } from "./session";

const props = defineProps<{ thread: ThreadView }>();
const narrow = useIsMobile();
const parts = computed(() => fixedParts(leadOf(props.thread), props.thread.session?.runtime, runtimeLabel));
const text = computed(() => (narrow.value ? parts.value.agent : [parts.value.node, parts.value.role, parts.value.agent].filter(Boolean).join(" · ")));
</script>

<template>
  <span
    v-if="parts.agent"
    :class="['inline-flex h-7 max-w-[260px] cursor-default items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground', narrow ? 'shrink-0' : 'min-w-0']"
    :title="`节点：${parts.node || '等待分配'}，岗位：${parts.role}，Agent：${parts.agent}（会话开始后不能再改）`"
    data-testid="session-fixed"
  >
    <Lock class="size-3 shrink-0" />
    <span class="truncate">{{ text }}</span>
  </span>
</template>
