<script setup lang="ts">
/** A deliverable published with publish_file / publish_deliverable: open it in the viewer (hosted) or its link. */
import { ExternalLink } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { DeliverableView, MessageView } from "@/api/views";
import { fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";
import { cn } from "@/lib/utils";
import { FILE_ICONS } from "../fileIcons";
import { CARD } from "./style";

const props = defineProps<{ message: MessageView; pid: string }>();
const d = computed(() => props.message.meta.deliverable);
const icon = computed(() => {
  const x = d.value;
  return x ? FILE_ICONS[fileIcon({ kind: x.kind, detail: { hosted: x.hosted, entry: x.entry } } as Pick<DeliverableView, "kind" | "detail">)] : null;
});
const look = computed(() => deliverableKind(d.value?.kind ?? ""));
const cls = cn(CARD, "flex max-w-[420px] items-center gap-3 px-3 py-2.5 hover:border-input");
</script>

<template>
  <component
    :is="d.hosted ? RouterLink : 'a'"
    v-if="d"
    v-bind="d.hosted ? { to: `/projects/${pid}/deliverables/${d.id}` } : { href: safeHref(d.url), target: '_blank', rel: 'noreferrer' }"
    :class="cls"
    :aria-label="`打开交付物 ${d.title}`"
  >
    <span class="flex size-10 shrink-0 items-center justify-center rounded-xl" :style="{ background: look.bg, color: look.fg }">
      <component :is="icon" aria-hidden="true" class="size-5" :stroke-width="1.7" />
    </span>
    <span class="flex min-w-0 flex-1 flex-col">
      <span class="truncate text-sm font-medium">{{ d.title }}</span>
      <span class="text-xs text-muted-foreground">{{ look.label }}{{ d.version ? ` · 第 ${d.version} 版` : "" }} · {{ message.meta.employee ?? message.author }} 发布</span>
    </span>
    <ExternalLink aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" :stroke-width="1.8" />
  </component>
</template>
