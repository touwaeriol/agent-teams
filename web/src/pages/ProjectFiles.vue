<script setup lang="ts">
// 共享文件: the project's folder in the user's shared folder (JuiceFS), the same tree employees see
// as .project-files/ in their working directories. Browse folders (breadcrumb), upload files or a
// folder into the current one (pick or drop, progress, one confirmation before replacing), create
// folders, download and delete. Until the user's shared folder is on, it says so and links to the
// setting. Works on phones: the table becomes stacked rows.
import { Download, Folder, FolderPlus, FolderUp, RefreshCw, Search, ShieldAlert, Trash2, Upload } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { useQueryClient } from "@tanstack/vue-query";
import { projectFiles as fileActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useProject, useProjectFiles, useSharedFolder } from "@/api/queries";
import type { FileEntry } from "@/api/views";
import Callout from "@/components/common/Callout.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import IconButton from "@/components/common/IconButton.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { formatBytes } from "@/lib/bytes";
import { formatDateTime } from "@/lib/library";
import {
  FILES_NOTICE,
  breadcrumbs,
  collectFiles,
  downloadUrl,
  droppedFiles,
  filterEntries,
  folderLinkText,
  isManaged,
  joinPath,
  pathProblem,
  planUpload,
  uploadErrorText,
  uploadProjectFiles,
  UploadBatchError,
  type SourceFile,
  type UploadPlan,
} from "@/lib/projectFiles";
import { toast } from "@/lib/toast";
import { UploadCancelled } from "@/lib/upload";
import { cn } from "@/lib/utils";

const route = useRoute();
const router = useRouter();
const pid = computed(() => String(route.params.pid ?? ""));
/** The folder shown, from ?dir= so the back button and links work. */
const dir = computed(() => (typeof route.query.dir === "string" ? route.query.dir : ""));
const project = useProject(pid);
const list = useProjectFiles(pid, dir);
const qc = useQueryClient();

const available = computed(() => list.data.value?.available ?? true);
const folder = useSharedFolder(() => !available.value);
const setupLink = computed(() => folderLinkText(folder.data.value));
const q = ref("");
watch(dir, () => (q.value = ""));
const entries = computed(() => filterEntries(list.data.value?.entries ?? [], q.value));
const crumbs = computed(() => breadcrumbs(dir.value));

function open(e: FileEntry) {
  if (e.dir) void router.push({ query: { dir: e.path } });
}
function goto(path: string) {
  void router.push({ query: path ? { dir: path } : {} });
}
const refresh = () => qc.invalidateQueries({ queryKey: keys.projectFiles(pid.value) });

const fileInput = ref<HTMLInputElement | null>(null);
const folderInput = ref<HTMLInputElement | null>(null);
const dragging = ref(false);
const problems = ref<string[]>([]);
const pending = ref<UploadPlan | null>(null);
const confirming = ref(false);
const progress = ref<number | null>(null);
let busy = false;
let uploading: AbortController | null = null;
const cancelUpload = () => uploading?.abort();
onBeforeUnmount(cancelUpload);

/** Shown when files are dropped while an upload is running (the drop is not queued). */
const BUSY_DROP = "正在上传，等这一批传完再拖入新的文件";
const EMPTY_PICK = "选中的文件夹是空的，没有可上传的文件";

/** Checks the picked files; asks once when some would be replaced, else uploads right away. */
function start(picked: (File | SourceFile)[] | null | undefined, notes: string[] = []) {
  if (!picked) return;
  if (busy) {
    toast.warning(BUSY_DROP);
    return;
  }
  const { files: chosen, skipped } = collectFiles(picked);
  if (skipped > 0) notes.push(`跳过了 ${skipped} 个系统文件（如 .DS_Store、.git）`);
  if (chosen.length === 0 && notes.length === 0) notes.push(EMPTY_PICK);
  if (notes.length > 0) toast.warning(notes.join("；"));
  if (chosen.length === 0) return;
  const plan = planUpload(chosen, list.data.value?.entries, dir.value);
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
  uploading = new AbortController();
  try {
    const done = await uploadProjectFiles(pid.value, dir.value, plan.files, (f) => (progress.value = f), uploading.signal);
    toast.success(`已上传 ${done.length} 个文件`);
    problems.value = [];
  } catch (e) {
    failed(e);
  } finally {
    busy = false;
    uploading = null;
    progress.value = null;
    pending.value = null;
    void refresh();
  }
}

function failed(e: unknown) {
  const partial = e instanceof UploadBatchError && e.done.length > 0;
  const cancelled = e instanceof UploadBatchError && e.cause instanceof UploadCancelled;
  if (cancelled && !partial) {
    toast.info("已取消上传");
    return;
  }
  problems.value = [uploadErrorText(e)];
  if (partial) toast.warning(`已上传 ${e.done.length} 个文件，其余没有上传`);
}

function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  start(el.files ? Array.from(el.files) : null);
  el.value = "";
}

async function dropped(e: DragEvent) {
  dragging.value = false;
  if (busy) {
    toast.warning(BUSY_DROP);
    return;
  }
  let got: Awaited<ReturnType<typeof droppedFiles>>;
  try {
    got = await droppedFiles(e.dataTransfer);
  } catch {
    // An entry of a dropped folder could not be read (permissions, a file removed meanwhile).
    toast.error("读取拖入的文件失败（有文件读不了），这次拖入没有上传；可以改用“上传文件夹”再试");
    return;
  }
  const notes = got.folders > 0 ? [`这个浏览器不能直接拖入文件夹（${got.folders} 个没有上传），请改用“上传文件夹”`] : [];
  start(got.files, notes);
}

// ConfirmAction closes (update:open) before it emits confirm, so the target is kept apart from the open flag.
const deleting = ref<FileEntry | null>(null);
const deleteOpen = ref(false);
function askDelete(e: FileEntry) {
  deleting.value = e;
  deleteOpen.value = true;
}
const remove = useAction((path: string) => fileActions.remove(pid.value, path), {
  invalidate: () => [keys.projectFiles(pid.value)],
  success: "已删除",
});
function confirmDelete() {
  if (deleting.value) remove.mutate(deleting.value.path);
}

const folderOpen = ref(false);
const folderName = ref("");
const folderError = ref("");
watch(folderOpen, (o) => {
  if (!o) return;
  folderName.value = "";
  folderError.value = "";
});
const mkdir = useAction((path: string) => fileActions.mkdir(pid.value, path), {
  invalidate: () => [keys.projectFiles(pid.value)],
  success: "已新建文件夹",
  onSuccess: () => (folderOpen.value = false),
});
function createFolder() {
  const name = folderName.value.trim();
  const path = joinPath(dir.value, name);
  folderError.value = pathProblem(name) ?? pathProblem(path) ?? "";
  if (!folderError.value) mkdir.mutate(path);
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
        <IconButton label="刷新" class="ml-auto" data-testid="files-refresh" @click="refresh">
          <RefreshCw :class="cn(list.isFetching.value && 'animate-spin')" />
        </IconButton>
      </div>

      <Callout v-if="!available" tone="warn" data-testid="files-unavailable">
        <strong class="font-semibold">{{ list.data.value?.reason }}</strong>
        <span class="block text-xs opacity-90">
          项目文件放在你的共享文件夹里。开通后，员工在工作目录的 .project-files/ 里就能直接读写它们。
          <RouterLink to="/settings/storage" class="underline">{{ setupLink }}</RouterLink>
        </span>
      </Callout>
      <Callout v-else tone="warn" data-testid="files-notice">
        <template #icon><ShieldAlert /></template>
        <strong class="font-semibold">{{ FILES_NOTICE }}</strong>
        <span class="block text-xs opacity-90">
          这里就是员工工作目录里的 .project-files/：在会话里工作的组长和成员都能直接读写，不进 git。同名文件上传后会被替换。
        </span>
      </Callout>

      <section
        v-if="available"
        :class="cn('relative flex min-h-[420px] min-w-0 flex-1 flex-col rounded-[14px] border bg-panel px-4 pt-3', dragging && 'border-primary')"
        data-testid="files-drop"
        @dragover.prevent="dragging = true"
        @dragleave.self="dragging = false"
        @drop.prevent="dropped"
      >
        <div class="flex shrink-0 flex-wrap items-center gap-2">
          <nav aria-label="当前位置" class="flex min-w-0 flex-wrap items-center gap-1 text-[13px]" data-testid="files-crumbs">
            <template v-for="(c, i) in crumbs" :key="c.path">
              <span v-if="i > 0" class="text-muted-foreground">/</span>
              <button
                type="button"
                :class="cn('rounded px-1 hover:bg-raised', i === crumbs.length - 1 ? 'font-medium text-foreground' : 'text-muted-foreground')"
                @click="goto(c.path)"
              >
                {{ c.name }}
              </button>
            </template>
          </nav>
          <span class="ml-auto flex min-w-0 flex-wrap items-center gap-2">
            <label class="flex h-8 min-w-0 items-center gap-2 rounded-lg border border-input bg-panel px-2.5 text-muted-foreground sm:w-48">
              <Search class="size-3.5 shrink-0" :stroke-width="1.8" />
              <input v-model="q" aria-label="筛选文件" placeholder="按名称筛选" class="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
            </label>
            <el-button size="small" @click="folderOpen = true"><FolderPlus /> 新建文件夹</el-button>
            <el-button size="small" :disabled="progress !== null" @click="folderInput?.click()"><FolderUp /> 上传文件夹</el-button>
            <el-button type="primary" size="small" :disabled="progress !== null" @click="fileInput?.click()"><Upload /> 上传文件</el-button>
          </span>
          <input ref="fileInput" type="file" multiple class="hidden" aria-label="选择要上传的文件" data-testid="files-input" @change="picked" />
          <input ref="folderInput" type="file" webkitdirectory class="hidden" aria-label="选择要上传的文件夹" @change="picked" />
        </div>

        <div v-if="progress !== null" class="mt-3 flex items-center gap-3 text-xs text-muted-foreground" role="progressbar" :aria-valuenow="Math.round(progress * 100)">
          <span class="h-1.5 flex-1 overflow-hidden rounded bg-raised"><span class="block h-full rounded bg-primary transition-all" :style="{ width: `${Math.round(progress * 100)}%` }" /></span>
          正在上传 {{ Math.round(progress * 100) }}%
          <el-button size="small" link data-testid="files-cancel" @click="cancelUpload">取消</el-button>
        </div>
        <Callout v-if="problems.length > 0" tone="bad" class="mt-3" data-testid="files-problems">
          <p class="font-medium">没有上传：</p>
          <ul class="list-disc pl-4">
            <li v-for="p in problems" :key="p" class="break-all">{{ p }}</li>
          </ul>
        </Callout>

        <div class="scroll-thin min-h-0 flex-1 overflow-y-auto py-3">
          <Loading v-if="list.isLoading.value" />
          <Callout v-else-if="list.error.value" tone="bad">{{ list.error.value.message }}</Callout>
          <div
            v-else-if="list.data.value && entries.length === 0"
            class="flex flex-col items-center gap-2 rounded-xl border border-dashed p-10 text-center text-[13px] text-muted-foreground"
            data-testid="files-empty"
          >
            <template v-if="q">没有名称包含“{{ q }}”的文件。</template>
            <template v-else>
              <Upload class="size-6" :stroke-width="1.6" />
              这里还没有文件。点“上传文件”或把文件拖到这里；员工也能直接写进 .project-files/。
            </template>
          </div>
          <div v-if="entries.length > 0" role="table" aria-label="项目文件" class="text-[13px]">
            <div role="row" class="sticky top-0 z-[1] hidden h-9 items-center gap-3 rounded-lg bg-raised px-3 text-xs font-medium text-foreground/80 md:flex">
              <span role="columnheader" class="min-w-0 flex-1">名称</span>
              <span role="columnheader" class="w-20 text-right">大小</span>
              <span role="columnheader" class="w-40">修改时间</span>
              <span class="w-16" />
            </div>
            <div
              v-for="e in entries"
              :key="e.path"
              role="row"
              class="group flex flex-wrap items-center gap-x-3 gap-y-0.5 rounded-lg px-3 py-2 hover:bg-raised/60 md:h-[40px] md:flex-nowrap md:py-0"
              :data-path="e.path"
            >
              <span role="cell" class="flex w-full min-w-0 items-center gap-2 md:w-auto md:flex-1">
                <button v-if="e.dir" type="button" class="flex min-w-0 items-center gap-2 text-left font-medium hover:underline" @click="open(e)">
                  <Folder class="size-4 shrink-0 text-primary" />
                  <span class="truncate">{{ e.name }}</span>
                </button>
                <span v-else class="truncate font-mono text-[12.5px] text-foreground" :title="e.path">{{ e.name }}</span>
              </span>
              <span role="cell" class="tabular-nums text-xs text-muted-foreground md:w-20 md:text-right md:text-[13px]">{{ e.dir ? "—" : formatBytes(e.size) }}</span>
              <span role="cell" class="tabular-nums text-xs text-muted-foreground md:w-40 md:text-[13px]">{{ e.dir ? "" : formatDateTime(e.mod_time) }}</span>
              <span class="ml-auto flex w-16 items-center justify-end gap-0.5">
                <a
                  v-if="!e.dir"
                  :href="downloadUrl(pid, e.path)"
                  download
                  :aria-label="`下载 ${e.path}`"
                  class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-foreground"
                >
                  <Download class="size-4" />
                </a>
                <button
                  v-if="!isManaged(e)"
                  type="button"
                  :aria-label="`删除 ${e.path}`"
                  class="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-bad"
                  @click="askDelete(e)"
                >
                  <Trash2 class="size-4" />
                </button>
              </span>
            </div>
          </div>
        </div>
        <div v-if="dragging" class="pointer-events-none absolute inset-0 flex items-center justify-center rounded-[14px] bg-primary/5 text-sm font-medium text-primary">
          松开鼠标上传到 {{ crumbs.at(-1)?.name }}
        </div>
      </section>
    </div>

    <ConfirmAction
      v-model:open="confirming"
      title="替换已有的文件？"
      :description="`这些文件或文件夹已经存在，同名文件上传后会被替换（不保留旧版本）：${pending?.overwrites.join('、') ?? ''}`"
      confirm-label="替换并上传"
      @confirm="pending && send(pending)"
    />
    <ConfirmAction
      v-model:open="deleteOpen"
      :title="deleting?.dir ? '删除这个文件夹？' : '删除这个文件？'"
      :description="`${deleting?.path ?? ''}${deleting?.dir ? ' 和里面的所有文件' : ''}会从项目文件里删除，员工工作目录的 .project-files/ 里也会同时消失。`"
      confirm-label="删除"
      @confirm="confirmDelete"
    />
    <FormDialog v-model:open="folderOpen" title="新建文件夹" submit-label="新建" :busy="mkdir.isPending.value" :disabled="!folderName.trim()" width="24rem" @submit="createFolder">
      <el-input v-model="folderName" placeholder="例如 inputs 或 data/raw" aria-label="文件夹名称" />
      <p v-if="folderError" class="text-[13px] text-destructive" role="alert">{{ folderError }}</p>
    </FormDialog>
  </main>
</template>
