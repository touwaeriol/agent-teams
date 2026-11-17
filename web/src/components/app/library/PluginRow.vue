<script setup lang="ts">
/** One plugin: name, latest version, 内置 / 已停用 badges, description, contents and upload time. */
import { computed } from "vue";
import type { PluginView } from "@/api/views";
import { formatBytes } from "@/lib/artifacts";
import { contentsText } from "@/lib/plugins";
import { timeAgo } from "@/lib/utils";

const props = defineProps<{ plugin: PluginView }>();
const latest = computed(() => props.plugin.versions?.[0]);
</script>

<template>
  <div class="flex items-start justify-between gap-2 rounded-[10px] border px-3 py-2.5" :data-plugin="plugin.name">
    <span class="flex min-w-0 flex-col gap-0.5">
      <span class="flex flex-wrap items-center gap-2 text-[13px] font-medium">
        <span class="font-mono">{{ plugin.name }}</span>
        <span class="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">v{{ plugin.latest }}{{ latest?.manifest_version ? ` · ${latest.manifest_version}` : "" }}</span>
        <span v-if="plugin.builtin" class="rounded-full bg-raised px-2 py-0.5 text-[11px] font-normal text-muted-foreground">内置</span>
        <span v-if="!plugin.enabled" class="rounded-full bg-[#F6E3E3] px-2 py-0.5 text-[11px] font-normal text-destructive dark:bg-destructive/15">已停用</span>
      </span>
      <span v-if="latest?.description" class="line-clamp-2 text-xs leading-[18px] text-muted-foreground">{{ latest.description }}</span>
      <span class="text-xs text-muted-foreground">{{ contentsText(latest?.contents) }}{{ latest ? ` · ${formatBytes(latest.size)} · ${timeAgo(latest.created_at)}上传` : "" }}</span>
    </span>
    <slot />
  </div>
</template>
