<script setup lang="ts">
// The right panel's list: 等你处理 / 进行中 / 已完成, then a collapsed 已归档 section. Rows have a ⋯ menu.
import { ChevronDown, ChevronRight } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { QuestionView, ThreadView } from "@/api/views";
import { liveLine } from "@/lib/live";
import { groupThreads, threadLine } from "@/lib/present";
import ThreadRow from "./ThreadRow.vue";

const props = withDefaults(defineProps<{ threads: ThreadView[]; waitingByThread?: Map<number, QuestionView>; menus?: boolean }>(), { menus: false, waitingByThread: undefined });
const emit = defineEmits<{ open: [no: number] }>();
const groups = computed(() => groupThreads(props.threads));
const archived = computed(() => props.threads.filter((t) => t.status === "archived").sort((a, b) => b.no - a.no));
const showArchived = ref(false);

/** A live thread's line prefers what is happening right now; others keep the summary line. */
function rowLine(t: ThreadView): string {
  const open = props.waitingByThread?.get(t.no);
  if (open) return threadLine(t, open);
  return liveLine(t) || threadLine(t, open);
}
const isLive = (t: ThreadView) => !props.waitingByThread?.get(t.no) && t.counts.waiting === 0;
</script>

<template>
  <p v-if="groups.length === 0 && archived.length === 0" class="text-[13px] leading-6 text-muted-foreground">还没有讨论串。和项目统筹说需求，它会开讨论串去做。</p>
  <div v-else class="flex flex-col gap-5">
    <section v-for="g in groups" :key="g.id" class="flex flex-col gap-0.5">
      <span class="pb-1.5 text-xs text-muted-foreground">{{ g.title }}</span>
      <ThreadRow v-for="t in g.items" :key="`${g.id}-${t.id}`" :thread="t" :line="rowLine(t)" :live="isLive(t)" :menu="menus" @open="(no) => emit('open', no)" />
    </section>
    <section v-if="archived.length > 0" class="flex flex-col gap-0.5">
      <button type="button" :aria-expanded="showArchived" class="flex items-center gap-1 self-start pb-1.5 text-xs text-muted-foreground hover:text-foreground" @click="showArchived = !showArchived">
        <ChevronDown v-if="showArchived" class="size-3.5" />
        <ChevronRight v-else class="size-3.5" />
        已归档 {{ archived.length }}
      </button>
      <template v-if="showArchived">
        <ThreadRow v-for="t in archived" :key="t.id" :thread="t" :line="t.summary || t.brief" :menu="menus" @open="(no) => emit('open', no)" />
      </template>
    </section>
  </div>
</template>
