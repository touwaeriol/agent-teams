<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { ThreadView } from "@/api/views";
import ProgressBar from "@/components/app/ProgressBar.vue";
import StatusPill from "@/components/app/StatusPill.vue";
import { leadOf, mainProgress, teamText } from "@/lib/present";
import { timeAgo } from "@/lib/utils";
import { cardLook, prText } from "./logic";

const props = defineProps<{ pid: string; thread: ThreadView }>();
const look = computed(() => cardLook(props.thread));
const progress = computed(() => mainProgress(props.thread));
</script>

<template>
  <RouterLink :to="`/projects/${pid}/threads/${thread.no}`" class="flex flex-col gap-2.5 rounded-xl border bg-panel px-[18px] py-4 hover:border-input">
    <span class="flex items-center justify-between gap-3">
      <span class="flex min-w-0 items-center gap-2">
        <span class="font-mono text-xs text-muted-foreground">#{{ thread.no }}</span>
        <span class="truncate font-semibold">{{ thread.title }}</span>
      </span>
      <StatusPill :tone="look.tone" :label="look.label" :spin="look.spin" />
    </span>
    <span class="text-[13px] leading-[21px] text-[#4A4843]">{{ thread.summary || thread.lead_last?.text || thread.brief }}</span>
    <span class="flex items-center gap-2.5">
      <ProgressBar :pct="progress.pct" :tone="look.tone" />
      <span class="whitespace-nowrap text-xs text-muted-foreground">主要任务 {{ progress.text }}</span>
    </span>
    <span class="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-muted-foreground">
      <span>组长 {{ leadOf(thread)?.name ?? "—" }}</span>
      <span>成员 {{ teamText(thread) }}</span>
      <span v-if="thread.branch" class="font-mono">{{ thread.branch }}</span>
      <span>{{ prText(thread) }}</span>
      <span>{{ timeAgo(thread.updated_at) }}</span>
    </span>
  </RouterLink>
</template>
