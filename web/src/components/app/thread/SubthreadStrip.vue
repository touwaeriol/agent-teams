<script setup lang="ts">
import { $t } from "@/i18n";
/** Under the open thread's header: its parent ("父讨论串 #3 标题") and its subthreads as small links with their status marks. */
import { ChevronRight, CornerLeftUp } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useThreads } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { subthreadsOf, threadMark } from "@/lib/threadList";
import ThreadMark from "../ThreadMark.vue";

const props = defineProps<{ pid: string; thread: ThreadView }>();
const threads = useThreads(() => props.pid);
const all = computed<ThreadView[]>(() => (Array.isArray(threads.data.value) ? threads.data.value : []));
const parent = computed(() => (props.thread.parent_no != null ? all.value.find((t) => t.no === props.thread.parent_no) : undefined));
const subs = computed(() => subthreadsOf(all.value, props.thread.no));
const href = (no: number) => `/projects/${props.pid}/threads/${no}`;
</script>

<template>
  <nav v-if="thread.parent_no != null || subs.length" :aria-label="$t('父子讨论串')" class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-[#EFEDE8] px-4 py-1.5 text-xs text-muted-foreground dark:border-border" data-subthread-strip="">
    <RouterLink v-if="thread.parent_no != null" :to="href(thread.parent_no)" class="flex min-w-0 items-center gap-1 hover:text-foreground" data-parent-link="">
      <CornerLeftUp class="size-3.5 shrink-0" :stroke-width="1.8" />
      <span class="truncate">{{ $t("父讨论串 #{parent_no}", { parent_no: thread.parent_no }) }} {{ parent?.title ?? "" }}</span>
    </RouterLink>
    <span v-if="subs.length" class="flex min-w-0 flex-wrap items-center gap-1.5">
      <span>{{ $t("子讨论串") }}</span>
      <RouterLink
        v-for="s in subs"
        :key="s.id"
        :to="href(s.no)"
        class="flex max-w-[14rem] items-center gap-1.5 rounded-md border bg-panel px-1.5 py-0.5 text-foreground/85 hover:border-input"
        :title="`#${s.no} ${s.title}`"
        data-sub-link=""
      >
        <ThreadMark :thread="s" :mark="threadMark(s)" class="!mt-0" />
        <span class="truncate">#{{ s.no }} {{ s.title }}</span>
        <ChevronRight class="size-3 shrink-0 text-[#8A877F]" :stroke-width="2" />
      </RouterLink>
    </span>
  </nav>
</template>
