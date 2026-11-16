<script setup lang="ts">
import { Square } from "lucide-vue-next";
import { computed } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import type { EmployeeBrief, ThreadView } from "@/api/views";
import { askingLabel, elapsed, isWorking } from "@/lib/live";
import { memberTone, RUNTIME_LABEL, TIER_LABEL } from "@/lib/present";
import LiveDot from "../LiveDot.vue";
import { memberStatusLine } from "./members";

const props = defineProps<{ thread: ThreadView; member: EmployeeBrief; asking: boolean; now: number }>();
const stop = useAction(() => threadActions.interruptMember(props.thread.id, props.member.name), { success: `已让 ${props.member.name} 停下` });
const working = computed(() => isWorking(props.member));
const where = computed(() => {
  const n = props.member.node;
  return `${RUNTIME_LABEL[props.member.runtime] ?? props.member.runtime} · ${n ? `${n.name}${n.online ? "" : "（离线）"}` : "未分配节点"}`;
});
const status = computed(() => memberStatusLine(askingLabel(props.member, props.asking), props.member.status, (since) => elapsed(since, props.now)));
</script>

<template>
  <div class="flex flex-col gap-1 rounded-lg border px-3 py-2.5">
    <span class="flex items-center gap-2 text-[13px]">
      <LiveDot :color="asking ? '#B4530B' : memberTone(member)" :on="working" />
      <span class="font-mono font-medium">{{ member.name }}</span>
      <span class="rounded bg-[#EFEDE8] px-1.5 py-px text-[11px] text-[#4A4843]">{{ member.tier === "lead" ? TIER_LABEL.lead : member.title || member.role }}</span>
      <button
        v-if="working"
        type="button"
        :disabled="stop.isPending.value"
        :aria-label="`让 ${member.name} 停下`"
        class="ml-auto flex h-7 items-center gap-1 rounded-md border px-2 text-xs hover:bg-background"
        @click="stop.mutate()"
      >
        <Square class="size-2.5 fill-current" /> 停下
      </button>
    </span>
    <span class="text-xs text-muted-foreground">{{ where }}</span>
    <span class="text-xs text-[#4A4843]">{{ status }}</span>
  </div>
</template>
