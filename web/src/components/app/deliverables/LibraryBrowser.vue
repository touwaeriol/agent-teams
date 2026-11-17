<script setup lang="ts">
// 资料库: the project's deliverables and published files as a table (or tiles), filtered by
// type, searchable (hosted images are not deliverables and are left out), with "+ 添加" to upload a file the team can read. Used as the right-hand
// panel of the main board, on the full Deliverables page and as the phone overview's 资料库 tab
// (`sheet`: no header of its own, the search box follows `search`).
import { Maximize2, Search, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useDeliverables } from "@/api/queries";
import IconButton from "@/components/common/IconButton.vue";
import Loading from "@/components/common/Loading.vue";
import { formatBytes, isLibraryDeliverable, nextSort, sortLibrary, type SortKey } from "@/lib/library";
import { cn } from "@/lib/utils";
import AddButton from "./AddButton.vue";
import { saveView, storedView, type LibraryView } from "./browser";
import LibraryGrid from "./LibraryGrid.vue";
import LibraryTable from "./LibraryTable.vue";
import TypeFilter from "./TypeFilter.vue";
import ViewToggle from "./ViewToggle.vue";

const props = defineProps<{ pid: string; variant: "panel" | "page" | "sheet"; search?: boolean }>();
const emit = defineEmits<{ close: [] }>();
const kind = ref("");
const q = ref("");
const ownSearch = ref(props.variant === "page");
const searching = computed({ get: () => (props.variant === "sheet" ? Boolean(props.search) : ownSearch.value), set: (v) => (ownSearch.value = v) });
const view = ref<LibraryView>(storedView());
const sort = ref<{ key: SortKey; dir: "asc" | "desc" }>({ key: "modified", dir: "desc" });
const list = useDeliverables(() => props.pid, kind, q, true);
const items = computed(() => sortLibrary((list.data.value?.items ?? []).filter(isLibraryDeliverable), sort.value.key, sort.value.dir));
/** Panel and sheet are padded; only the panel has its own header. */
const panel = computed(() => props.variant !== "page");
const pick = (v: LibraryView) => {
  view.value = v;
  saveView(v);
};
/** The panel's search box takes focus when it opens (React autoFocus). */
const vFocus = { mounted: (el: HTMLInputElement) => panel.value && el.focus() };
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <div v-if="variant === 'panel'" class="flex h-14 shrink-0 items-center gap-1 border-b px-4">
      <span class="flex-1 font-semibold">资料库</span>
      <IconButton label="搜索" :active="searching" @click="searching = !searching"><Search :stroke-width="1.8" /></IconButton>
      <IconButton label="全屏打开" :to="`/projects/${pid}/deliverables`"><Maximize2 :stroke-width="1.8" /></IconButton>
      <IconButton label="关闭资料库" @click="emit('close')"><X :stroke-width="1.8" /></IconButton>
    </div>
    <div :class="cn('flex shrink-0 flex-wrap items-center gap-2', panel ? 'px-4 pt-3' : '')">
      <TypeFilter :value="kind" @change="(v) => (kind = v)" />
      <label v-if="searching" class="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border border-input bg-panel px-2.5 text-muted-foreground sm:max-w-60">
        <Search class="size-3.5 shrink-0" :stroke-width="1.8" />
        <input v-focus v-model="q" placeholder="搜索资料库" class="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
      </label>
      <span class="ml-auto flex items-center gap-2">
        <slot name="extra" />
        <ViewToggle :value="view" @change="pick" />
        <AddButton :pid="pid" />
      </span>
    </div>
    <div :class="cn('scroll-thin min-h-0 flex-1 overflow-y-auto', panel ? 'px-4 pb-3 pt-2' : 'pt-3')">
      <Loading v-if="list.isLoading.value" />
      <p v-if="list.data.value && items.length === 0" class="rounded-xl border border-dashed p-8 text-center text-[13px] text-muted-foreground">
        {{ q || kind ? "没有匹配的内容。" : "资料库还是空的。员工发布的设计稿、文档、PR 会出现在这里，你也可以点“添加”上传文件。" }}
      </p>
      <LibraryTable v-if="items.length > 0 && view === 'list'" :pid="pid" :items="items" :sort="sort" @sort="(k) => (sort = nextSort(sort, k))" />
      <LibraryGrid v-if="items.length > 0 && view === 'grid'" :pid="pid" :items="items" />
    </div>
    <div :class="cn('flex shrink-0 items-center border-t py-2.5 text-xs text-muted-foreground', panel ? 'px-4' : '')">
      已使用 {{ formatBytes(list.data.value?.storage_bytes ?? 0) }}
      <span class="ml-auto">{{ items.length }} 项</span>
    </div>
  </div>
</template>
