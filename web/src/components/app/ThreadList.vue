<script setup lang="ts">
// The right panel's list: collapsible status groups ("进行中 · 1 ⌄": 进行中 / 已解决 / 失败 /
// 已暂停 / 已归档, empty ones hidden) with counts; folding is the caller's (remembered per user).
// Rows have a ⋯ menu. 进行中 and 失败 rows are rich (progress line, node, tags); 已解决 / 已暂停 /
// 已归档 are compact single lines. Inside 进行中 the mark tells waiting-on-you / working / idle apart.
import { ChevronDown } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref } from "vue";
import type { QuestionView, ThreadView } from "@/api/views";
import { filterThreads, groupThreads, isCompactGroup, NO_FILTER, rowLine, threadMark, type Folded, type ListFilter, type ListGroupId } from "@/lib/threadList";
import ThreadRow from "./ThreadRow.vue";

const props = withDefaults(
  defineProps<{ threads: ThreadView[]; waitingByThread?: Map<number, QuestionView>; menus?: boolean; filter?: ListFilter; folded?: Folded }>(),
  { menus: false, waitingByThread: undefined, filter: () => NO_FILTER, folded: () => ({ archived: true }) },
);
const emit = defineEmits<{ open: [no: number]; fold: [id: ListGroupId, folded: boolean] }>();
const shown = computed(() => filterThreads(props.threads, props.filter));
const groups = computed(() => groupThreads(shown.value, props.waitingByThread));
/** While searching every group is open, so matches are not hidden. */
const isFolded = (id: ListGroupId) => !props.filter.q.trim() && Boolean(props.folded[id]);
const now = ref(Date.now());
const tick = setInterval(() => (now.value = Date.now()), 60_000);
onBeforeUnmount(() => clearInterval(tick));
</script>

<template>
  <p v-if="threads.length === 0" class="text-[13px] leading-6 text-muted-foreground">还没有讨论串。和项目统筹说需求，它会开讨论串去做。</p>
  <p v-else-if="groups.length === 0" class="text-[13px] leading-6 text-muted-foreground">没有符合条件的讨论串。</p>
  <div v-else class="flex flex-col gap-5">
    <section v-for="g in groups" :key="g.id" :class="['flex flex-col', isCompactGroup(g.id) ? 'gap-0' : 'gap-1']" :data-group="g.id">
      <button
        type="button"
        :aria-expanded="!isFolded(g.id)"
        class="-ml-1.5 mb-1 flex items-center gap-1 self-start rounded-md px-1.5 py-[3px] text-xs text-muted-foreground hover:bg-raised hover:text-foreground"
        @click="emit('fold', g.id, !isFolded(g.id))"
      >
        {{ g.label }} · {{ g.items.length }}
        <ChevronDown class="size-3.5 transition-transform" :class="isFolded(g.id) && '-rotate-90'" :stroke-width="1.8" />
      </button>
      <template v-if="!isFolded(g.id)">
        <ThreadRow
          v-for="t in g.items"
          :key="t.id"
          :thread="t"
          :mark="threadMark(t, waitingByThread)"
          :line="rowLine(t, waitingByThread?.get(t.no))"
          :now="now"
          :menu="menus"
          :compact="isCompactGroup(g.id)"
          @open="(no) => emit('open', no)"
        />
      </template>
    </section>
  </div>
</template>
