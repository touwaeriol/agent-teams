<script setup lang="ts">
// 共享文件 (api.md §3.5.1): the project's shared files — upload (pick or drop, optional folder
// prefix, progress, one confirmation before overwriting), list with usage and limits, search,
// download and delete. Refreshes on shared_file events (events.ts). Works on phones: the table
// becomes stacked rows.
import { Download, FolderUp, Search, ShieldAlert, Trash2, Upload } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { sharedFiles as sharedActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useProject, useSharedFiles } from "@/api/queries";
import { useQueryClient } from "@tanstack/vue-query";
import Callout from "@/components/common/Callout.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { formatDateTime } from "@/lib/library";
import {
  SHARED_LIMITS,
  SHARED_NOTICE,
  downloadUrl,
  filterShared,
  formatSize,
  pickedFiles,
  planUpload,
  uploadErrorText,
  uploaderLabel,
  uploadSharedFiles,
  type UploadPlan,
} from "@/lib/sharedFiles";
import { toast } from "@/lib/toast";
import { cn } from "@/lib/utils";

const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const list = useSharedFiles(pid);
const qc = useQueryClient();

const q = ref("");
const dir = ref("");
const files = computed(() => filterShared(list.data.value?.files ?? [], q.value));
const limits = computed(() => list.data.value?.limits ?? SHARED_LIMITS);
const total = computed(() => list.data.value?.total_bytes ?? 0);
const usedPct = computed(() => `${Math.min(100, (total.value / limits.value.project_bytes) * 100).toFixed(1)}%`);

const fileInput = ref<HTMLInputElement | null>(null);
const folderInput = ref<HTMLInputElement | null>(null);
const dragging = ref(false);
const problems = ref<string[]>([]);
const pending = ref<UploadPlan | null>(null);
const confirming = ref(false);
const progress = ref<number | null>(null);
let busy = false;

/** Checks the picked files; asks once when some would be overwritten, else uploads right away. */
function start(picked: FileList | File[] | null | undefined) {
  if (!picked || picked.length === 0 || busy) return;
  const plan = planUpload(pickedFiles(picked, dir.value), list.data.value);
  problems.value = plan.problems;
  if (plan.problems.length > 0) return;
  if (plan.overwrites.length > 0) {
    pending.value = plan;
    confirming.value = true;
    return;
  }
  void send(plan);
}

async function send(plan: UploadPlan) {
  if (busy) return;
  busy = true;
  progress.value = 0;
  try {
    const res = await uploadSharedFiles(pid.value, plan.files, (f) => (progress.value = f));
    const replaced = (res.files ?? []).filter((f) => f.overwritten).length;
    toast.success(replaced ? `已上传 ${res.files?.length ?? 0} 个文件（覆盖 ${replaced} 个）` : `已上传 ${res.files?.length ?? 0} 个文件`);
    problems.value = [];
  } catch (e) {
    problems.value = [uploadErrorText(e)];
  } finally {
    busy = false;
    progress.value = null;
    pending.value = null;
    void qc.invalidateQueries({ queryKey: keys.sharedFiles(pid.value) });
  }
}

function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  start(el.files ? Array.from(el.files) : null);
  el.value = "";
}

function dropped(e: DragEvent) {
  dragging.value = false;
  start(e.dataTransfer?.files ? Array.from(e.dataTransfer.files) : null);
}

const deleting = ref<string | null>(null);
const deleteOpen = computed({ get: () => deleting.value !== null, set: (v) => !v && (deleting.value = null) });
const remove = useAction((path: string) => sharedActions.remove(pid.value, path), {
  invalidate: () => [keys.sharedFiles(pid.value)],
  success: "已删除",
});
function confirmDelete() {
  if (deleting.value) remove.mutate(deleting.value);
  deleting.value = null;
}
</script>

<template>
  <main class="flex min-h-full flex-col px-4 pb-6 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex w-full min-w-0 max-w-[1080px] flex-1 flex-col gap-4">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1">
          <RouterLink :to="`/projects/${pid}`" class="text-xs text-muted-foreground hover:text-foreground">{{ project.data.value?.name ?? "项目" }}</RouterLink>
          <h1 class="text-[22px] font-semibold">共享文件</h1>
        </div>
      </div>

      <Callout tone="warn" data-testid="shared-notice">
        <template #icon><ShieldAlert /></template>
        <strong class="font-semibold">{{ SHARED_NOTICE }}</strong>
        <span class="block text-xs opacity-90">
          文件存在平台上、不进 git，项目里的统筹、组长和成员都能用工具读写；同名文件会直接覆盖。单个文件不超过 {{ formatSize(limits.file_bytes) }}，项目合计不超过
          {{ formatSize(limits.project_bytes) }}、最多 {{ limits.project_files }} 个。
        </span>
      </Callout>

      <section
        :class="cn('relative flex min-h-[420px] min-w-0 flex-1 flex-col rounded-[14px] border bg-panel px-4 pt-3', dragging && 'border-primary')"
        data-testid="shared-drop"
        @dragover.prevent="dragging = true"
        @dragleave.self="dragging = false"
        @drop.prevent="dropped"
      >
        <div class="flex shrink-0 flex-wrap items-center gap-2">
          <label class="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border border-input bg-panel px-2.5 text-muted-foreground sm:max-w-60">
            <Search class="size-3.5 shrink-0" :stroke-width="1.8" />
            <input v-model="q" aria-label="搜索共享文件" placeholder="按路径搜索或筛选（如 data/）" class="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
          </label>
          <span class="ml-auto flex min-w-0 flex-wrap items-center gap-2">
            <label class="flex h-8 min-w-0 items-center gap-1.5 rounded-lg border border-input px-2.5 text-xs text-muted-foreground">
              上传到
              <input v-model="dir" aria-label="目录前缀" placeholder="根目录（可填 data/）" class="w-36 min-w-0 bg-transparent text-[13px] text-foreground outline-none" />
            </label>
            <el-button size="small" :disabled="progress !== null" @click="folderInput?.click()"><FolderUp /> 上传文件夹</el-button>
            <el-button type="primary" size="small" :disabled="progress !== null" @click="fileInput?.click()"><Upload /> 上传文件</el-button>
          </span>
          <input ref="fileInput" type="file" multiple class="hidden" aria-label="选择要上传的文件" data-testid="shared-file-input" @change="picked" />
          <input ref="folderInput" type="file" webkitdirectory class="hidden" aria-label="选择要上传的文件夹" @change="picked" />
        </div>

        <div v-if="progress !== null" class="mt-3 flex items-center gap-3 text-xs text-muted-foreground" role="progressbar" :aria-valuenow="Math.round(progress * 100)">
          <span class="h-1.5 flex-1 overflow-hidden rounded bg-raised"><span class="block h-full rounded bg-primary transition-all" :style="{ width: `${Math.round(progress * 100)}%` }" /></span>
          正在上传 {{ Math.round(progress * 100) }}%
        </div>
        <Callout v-if="problems.length > 0" tone="bad" class="mt-3" data-testid="shared-problems">
          <p class="font-medium">没有上传：</p>
          <ul class="list-disc pl-4">
            <li v-for="p in problems" :key="p" class="break-all">{{ p }}</li>
          </ul>
        </Callout>

        <div class="scroll-thin min-h-0 flex-1 overflow-y-auto pt-3">
          <Loading v-if="list.isLoading.value" />
          <div
            v-if="list.data.value && files.length === 0"
            class="flex flex-col items-center gap-2 rounded-xl border border-dashed p-10 text-center text-[13px] text-muted-foreground"
            data-testid="shared-empty"
          >
            <template v-if="q">没有路径包含“{{ q }}”的文件。</template>
            <template v-else>
              <Upload class="size-6" :stroke-width="1.6" />
              还没有共享文件。点“上传文件”或把文件拖到这里；员工也能用 write_shared_file 写进来。
            </template>
          </div>
          <div v-if="files.length > 0" role="table" aria-label="共享文件" class="text-[13px]">
            <div role="row" class="sticky top-0 z-[1] hidden h-9 items-center gap-3 rounded-lg bg-raised px-3 text-xs font-medium text-foreground/80 md:flex">
              <span role="columnheader" class="min-w-0 flex-1">路径</span>
              <span role="columnheader" class="w-20 text-right">大小</span>
              <span role="columnheader" class="w-28">上传者</span>
              <span role="columnheader" class="w-40">更新时间</span>
              <span class="w-16" />
            </div>
            <div
              v-for="f in files"
              :key="f.path"
              role="row"
              class="group flex flex-wrap items-center gap-x-3 gap-y-0.5 rounded-lg px-3 py-2 hover:bg-raised/60 md:h-[40px] md:flex-nowrap md:py-0"
              :data-path="f.path"
            >
              <span role="cell" class="w-full min-w-0 truncate font-mono text-[12.5px] text-foreground md:w-auto md:flex-1" :title="f.path">{{ f.path }}</span>
              <span role="cell" class="tabular-nums text-xs text-muted-foreground md:w-20 md:text-right md:text-[13px]">{{ formatSize(f.size) }}</span>
              <span role="cell" class="truncate text-xs text-muted-foreground md:w-28 md:text-[13px]">{{ uploaderLabel(f) }}</span>
              <span role="cell" class="tabular-nums text-xs text-muted-foreground md:w-40 md:text-[13px]">{{ formatDateTime(f.updated_at) }}</span>
              <span class="ml-auto flex w-16 items-center justify-end gap-0.5">
                <a
                  :href="downloadUrl(pid, f.path)"
                  download
                  :aria-label="`下载 ${f.path}`"
                  class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-foreground"
                >
                  <Download class="size-4" />
                </a>
                <button
                  type="button"
                  :aria-label="`删除 ${f.path}`"
                  class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-bad"
                  @click="deleting = f.path"
                >
                  <Trash2 class="size-4" />
                </button>
              </span>
            </div>
          </div>
        </div>

        <div class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t py-2.5 text-xs text-muted-foreground" data-testid="shared-usage">
          <span>已用 {{ formatSize(total) }} / {{ formatSize(limits.project_bytes) }}</span>
          <span class="flex h-1.5 w-24 overflow-hidden rounded bg-raised"><span class="block h-full bg-primary" :style="{ width: usedPct }" /></span>
          <span class="ml-auto">{{ list.data.value?.file_count ?? 0 }} / {{ limits.project_files }} 个文件</span>
        </div>
        <div v-if="dragging" class="pointer-events-none absolute inset-0 flex items-center justify-center rounded-[14px] bg-primary/5 text-sm font-medium text-primary">
          松开鼠标上传到 {{ dir.trim() || "根目录" }}
        </div>
      </section>
    </div>

    <ConfirmAction
      v-model:open="confirming"
      title="覆盖已有的共享文件？"
      :description="`这些文件已经存在，上传后会被替换（不保留旧版本）：${pending?.overwrites.join('、') ?? ''}`"
      confirm-label="覆盖并上传"
      @confirm="pending && send(pending)"
    />
    <ConfirmAction
      v-model:open="deleteOpen"
      title="删除这个共享文件？"
      :description="`${deleting ?? ''} 会从项目共享文件里删除，所有会话和成员都取不到它了（已经取到各工作目录里的副本不受影响）。`"
      confirm-label="删除"
      @confirm="confirmDelete"
    />
  </main>
</template>
