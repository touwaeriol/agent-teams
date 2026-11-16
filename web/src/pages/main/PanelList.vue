<script setup lang="ts">
// The right panel before a thread is opened: "讨论串" with search, filter, expand and close;
// the project's newest deliverables; then the thread list in status groups (folds per user).
import { Filter, Maximize2, Minimize2, Search, X } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useWaiting } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import DeliverableStrip from "@/components/app/DeliverableStrip.vue";
import ThreadList from "@/components/app/ThreadList.vue";
import IconButton from "@/components/common/IconButton.vue";
import Loading from "@/components/common/Loading.vue";
import { filterActive, LIST_GROUPS, loadFolded, saveFolded, type Folded, type ListGroupId } from "@/lib/threadList";
import { waitingIndex } from "./logic";

const props = defineProps<{ pid: string; threads: ThreadView[]; loading: boolean; userId: string; expanded?: boolean }>();
const emit = defineEmits<{ open: [no: number]; expand: [] }>();
const router = useRouter();
const waiting = useWaiting(() => props.pid);
const index = computed(() => waitingIndex(waiting.data.value?.questions));

const searching = ref(false);
const q = ref("");
const groups = ref<ListGroupId[]>([]);
const withDeliverable = ref(false);
const filter = computed(() => ({ q: q.value, groups: groups.value, withDeliverable: withDeliverable.value }));
const filtering = computed(() => filterActive(filter.value));

const folded = ref<Folded>(loadFolded(props.userId));
watch(() => props.userId, (u) => (folded.value = loadFolded(u)));
function fold(id: string, value: boolean) {
  folded.value = { ...folded.value, [id]: value };
  saveFolded(props.userId, folded.value);
}

function toggleSearch() {
  searching.value = !searching.value;
  if (!searching.value) q.value = "";
}
function clearFilter() {
  groups.value = [];
  withDeliverable.value = false;
}
const library = () => void router.replace({ query: { panel: "library" } });
const close = () => void router.push(`/projects/${props.pid}?panel=none`);
</script>

<template>
  <header class="flex h-14 shrink-0 items-center gap-0.5 border-b border-[#EFEDE8] pl-5 pr-2.5">
    <nav aria-label="位置" class="flex-1 font-semibold">讨论串</nav>
    <IconButton label="搜索讨论串" :active="searching" @click="toggleSearch"><Search :stroke-width="1.8" /></IconButton>
    <el-popover trigger="click" placement="bottom-end" :width="220" :show-arrow="false" popper-class="!p-3">
      <template #reference>
        <span class="relative inline-flex">
          <IconButton label="筛选" :active="filtering"><Filter :stroke-width="1.8" /></IconButton>
          <span v-if="filtering" class="pointer-events-none absolute right-1.5 top-1.5 size-1.5 rounded-full bg-[#C2570C]" aria-hidden="true" />
        </span>
      </template>
      <div class="flex flex-col gap-2 text-[13px]" aria-label="筛选讨论串">
        <span class="text-xs text-muted-foreground">状态</span>
        <el-checkbox-group v-model="groups" class="flex flex-col">
          <el-checkbox v-for="g in LIST_GROUPS" :key="g.id" :value="g.id" :label="g.label" class="!h-7" />
        </el-checkbox-group>
        <span class="border-t pt-2 text-xs text-muted-foreground">交付物</span>
        <el-checkbox v-model="withDeliverable" label="有交付物" class="!h-7" />
        <button v-if="filtering" type="button" class="self-start text-xs text-muted-foreground underline-offset-2 hover:underline" @click="clearFilter">清除筛选</button>
      </div>
    </el-popover>
    <IconButton :label="expanded ? '收起面板' : '展开面板'" @click="emit('expand')">
      <Minimize2 v-if="expanded" :stroke-width="1.8" />
      <Maximize2 v-else :stroke-width="1.8" />
    </IconButton>
    <IconButton label="关闭" @click="close"><X :stroke-width="1.8" /></IconButton>
  </header>
  <div v-if="searching" class="shrink-0 border-b border-[#EFEDE8] px-4 py-2">
    <el-input v-model="q" size="small" placeholder="搜索标题或进展" clearable autofocus aria-label="搜索讨论串" @keydown.esc="toggleSearch">
      <template #prefix><Search class="size-3.5" /></template>
    </el-input>
  </div>
  <div class="scroll-thin flex flex-1 flex-col gap-5 overflow-y-auto px-5 pb-6 pt-4">
    <Loading v-if="loading" />
    <template v-else>
      <DeliverableStrip v-if="!q.trim() && !filtering" :pid="pid" :folded="Boolean(folded.deliverables)" @fold="(v) => fold('deliverables', v)" @all="library" />
      <ThreadList :threads="threads" :waiting-by-thread="index" :filter="filter" :folded="folded" menus @open="(no) => emit('open', no)" @fold="fold" />
    </template>
  </div>
</template>
