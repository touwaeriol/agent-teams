<script setup lang="ts">
// Phones: the project overview over the chat (?sheet=threads|library|schedules). A segmented
// control 讨论串 / 资料库 / 定时任务 on top with ✕ on the left and search on the right; under
// it the thread list (交付物, 进行中, 失败, then compact 已解决 / 已暂停 / 已归档), the library or
// the scheduled tasks.
import { Search, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { ThreadView } from "@/api/views";
import LibraryBrowser from "@/components/app/deliverables/LibraryBrowser.vue";
import SchedulesPanel from "@/components/app/schedules/SchedulesPanel.vue";
import IconButton from "@/components/common/IconButton.vue";
import { cn } from "@/lib/utils";
import { listView } from "@/lib/threadListView";
import { OVERVIEW_TABS, type OverviewTab } from "./logic";
import PanelList from "./PanelList.vue";

const props = defineProps<{ pid: string; tab: OverviewTab; threads: ThreadView[]; loading: boolean; userId: string }>();
const emit = defineEmits<{ tab: [tab: OverviewTab]; open: [no: number]; close: [] }>();
const view = computed(() => listView(props.pid));
const librarySearch = ref(false);
const searching = computed(() => (props.tab === "threads" ? view.value.searching : props.tab === "library" ? librarySearch.value : false));
function toggleSearch() {
  if (props.tab === "library") return void (librarySearch.value = !librarySearch.value);
  view.value.searching = !view.value.searching;
  if (!view.value.searching) view.value.q = "";
}
</script>

<template>
  <section aria-label="项目概览" class="fixed inset-0 z-30 flex flex-col bg-panel lg:hidden">
    <header class="flex h-14 shrink-0 items-center gap-2 border-b border-[#EFEDE8] px-2.5">
      <IconButton label="关闭" @click="emit('close')"><X :stroke-width="1.8" /></IconButton>
      <div class="flex min-w-0 flex-1 justify-center">
        <div role="tablist" aria-label="项目概览" class="flex h-9 items-center rounded-full bg-raised p-[3px]">
          <button
            v-for="t in OVERVIEW_TABS"
            :key="t.id"
            type="button"
            role="tab"
            :aria-selected="tab === t.id"
            :class="cn('h-[30px] rounded-full px-3.5 text-[13px] transition-colors', tab === t.id ? 'bg-panel font-medium text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.08)]' : 'text-muted-foreground')"
            @click="emit('tab', t.id)"
          >
            {{ t.label }}
          </button>
        </div>
      </div>
      <IconButton v-if="tab !== 'schedules'" :label="tab === 'threads' ? '搜索讨论串' : '搜索资料库'" :active="searching" @click="toggleSearch"><Search :stroke-width="1.8" /></IconButton>
      <span v-else class="size-8 shrink-0" aria-hidden="true" />
    </header>
    <div v-if="tab === 'threads'" class="flex min-h-0 flex-1 flex-col">
      <PanelList :pid="pid" :threads="threads" :loading="loading" :user-id="userId" sheet @open="(no) => emit('open', no)" @library="emit('tab', 'library')" />
    </div>
    <LibraryBrowser v-else-if="tab === 'library'" :pid="pid" variant="sheet" :search="librarySearch" />
    <div v-else class="scroll-thin flex-1 overflow-y-auto px-4 pb-6 pt-4">
      <SchedulesPanel :pid="pid" />
    </div>
  </section>
</template>
