<script setup lang="ts">
/** "讨论串 > ● #12 标题" with ✓ (archive), ⋯ (任务 / 交付物 / 成员 / 全屏 / 暂停 / 归档 / 删除), expand and close. */
import { ChevronRight, Maximize2, Minimize2, X } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import IconButton from "@/components/common/IconButton.vue";
import { threadLook } from "@/lib/present";
import Dot from "../Dot.vue";
import ResolveButton from "../ResolveButton.vue";
import Spin from "../Spin.vue";
import ThreadMenu from "../ThreadMenu.vue";
import type { ThreadViewId } from "../threadMenu";
import type { ExpandLink } from "./detail";

const props = defineProps<{ thread: ThreadView; expand?: ExpandLink; fullHref?: string }>();
const emit = defineEmits<{ back: []; close: []; view: [view: ThreadViewId] }>();
const look = computed(() => threadLook(props.thread));
</script>

<template>
  <header class="flex h-14 shrink-0 items-center gap-0.5 border-b border-[#EFEDE8] px-2.5">
    <nav aria-label="位置" class="flex min-w-0 flex-1 items-center gap-0.5 text-[13px]">
      <button type="button" class="h-8 shrink-0 rounded-lg px-2 text-muted-foreground hover:bg-raised hover:text-foreground" @click="emit('back')">讨论串</button>
      <ChevronRight class="size-3.5 shrink-0 text-[#B7B3AA]" :stroke-width="2" />
      <span class="flex min-w-0 items-center gap-1.5 px-1" :title="`#${thread.no} ${thread.title} · ${look.label}`">
        <Spin v-if="look.spin" :color="look.tone" :size="11" :label="look.label" />
        <template v-else>
          <Dot :color="look.tone" :size="7" class="mx-0.5" />
          <span class="sr-only">{{ look.label }}</span>
        </template>
        <span aria-current="page" class="truncate font-medium">#{{ thread.no }} {{ thread.title }}</span>
      </span>
    </nav>
    <ResolveButton :thread="thread" />
    <ThreadMenu :thread="thread" :full-href="fullHref" with-views @view="(v) => emit('view', v)" @deleted="emit('back')" />
    <IconButton v-if="expand" :label="expand.label" :to="expand.to">
      <Minimize2 v-if="expand.collapse" :stroke-width="1.8" />
      <Maximize2 v-else :stroke-width="1.8" />
    </IconButton>
    <IconButton label="关闭" @click="emit('close')"><X :stroke-width="1.8" /></IconButton>
  </header>
</template>
