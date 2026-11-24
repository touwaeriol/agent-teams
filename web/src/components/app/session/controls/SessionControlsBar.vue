<script setup lang="ts">
/**
 * The session box's controls (#53): "模型 · 强度" and the permission mode chip, from
 * GET /api/projects/{pid}/session-controls. A pick goes out with PUT and shows 切换中… until the
 * server writes back what the session runs (session_controls event; polled meanwhile). While the
 * mode menu is open the digits 1..n pick its rows. Narrow screens get one chip with all three
 * that opens a bottom sheet. Hidden when the server has no controls for the conversation.
 */
import { useQueryClient } from "@tanstack/vue-query";
import { ChevronDown, Loader2 } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { sessions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSessionControls } from "@/api/queries";
import { useIsMobile } from "@/lib/media";
import { runtimeLabel } from "@/lib/runtimes";
import {
  MODE_LOOK,
  TONE_CLASS,
  appliesNote,
  effortFor,
  isMode,
  modeRows,
  modelChipText,
  modelOf,
  noAutoNote,
  rowForKey,
  type SessionControls,
} from "@/lib/sessionControls";
import { toast } from "@/lib/toast";
import ModeIcon from "./ModeIcon.vue";
import ModeMenuBody from "./ModeMenuBody.vue";
import ModelMenuBody from "./ModelMenuBody.vue";

const props = defineProps<{ pid: string }>();
const qc = useQueryClient();
const q = useSessionControls(() => props.pid);
const c = computed<SessionControls | undefined>(() => q.data.value);
const narrow = useIsMobile();
const modeOpen = ref(false);
const modelOpen = ref(false);
const sheet = ref(false);

/** What was just sent (before the server answers the PUT). */
const sent = ref<{ permission_mode?: string; model?: string; effort?: string } | null>(null);
const pending = computed(() => c.value?.pending ?? sent.value ?? null);
const pendingMode = computed(() => pending.value?.permission_mode ?? "");
const busy = computed(() => Boolean(pending.value));
const rows = computed(() => modeRows(c.value?.modes));
const models = computed(() => c.value?.models ?? []);
const efforts = computed(() => c.value?.efforts ?? []);
const runtime = computed(() => c.value?.runtime ?? "claude");
const mode = computed(() => c.value?.permission_mode ?? "default");
const look = computed(() => (isMode(mode.value) ? MODE_LOOK[mode.value] : MODE_LOOK.default));
const modeLabel = (id: string) => rows.value.find((r) => r.id === id)?.label ?? (isMode(id) ? MODE_LOOK[id].short : id);
const modelText = computed(() => (c.value ? modelChipText(models.value, c.value.model, c.value.effort, runtime.value) : ""));
const modeNotes = computed(() => {
  const v = c.value;
  if (!v) return [];
  return [
    noAutoNote(runtime.value, models.value, v.model),
    v.node_outdated ? "会话所在节点的守护进程较旧，只能用这几档；升级节点后可用全部选项。" : "",
    v.error ? `上次切换没有成功：${v.error}` : "",
    `${appliesNote(runtime.value, v.modes)}选择记在这个会话上，重开会话沿用，不改岗位设置。`,
  ].filter(Boolean);
});
const modelNote = computed(() => {
  const now = (c.value?.modes ?? []).some((m) => m.applies === "now") || runtime.value === "claude";
  return `${now ? "立即生效" : "下一轮生效"}，只改这个会话，不改岗位设置。${c.value?.node_outdated ? "节点较旧，暂时不能切换思考强度。" : ""}`;
});

const put = useAction((body: { permission_mode?: string; model?: string; effort?: string }) => sessions.setControls(props.pid, body as never), {
  onSuccess: (data) => {
    sent.value = null;
    qc.setQueryData(keys.sessionControls(props.pid), data);
  },
});
watch(put.isError, (e) => e && (sent.value = null));
function send(body: { permission_mode?: string; model?: string; effort?: string }) {
  if (busy.value || put.isPending.value) return;
  sent.value = body;
  put.mutate(body);
}
function pickMode(id: string) {
  if (id === mode.value) return void (modeOpen.value = false);
  send({ permission_mode: id });
}
function pickModel(id: string) {
  const eff = effortFor(modelOf(models.value, id), c.value?.effort ?? "");
  send(eff && eff !== c.value?.effort && !c.value?.node_outdated ? { model: id, effort: eff } : { model: id });
}
function pickEffort(e: string) {
  send({ effort: e });
}

// A switch waiting for the node: poll until the server has its answer (the event usually comes first).
let timer: ReturnType<typeof setInterval> | undefined;
watch(
  () => Boolean(c.value?.pending),
  (on) => {
    clearInterval(timer);
    timer = on ? setInterval(() => void q.refetch(), 2000) : undefined;
  },
  { immediate: true },
);
onBeforeUnmount(() => clearInterval(timer));
// The switch went through (or failed): the menus close.
watch(busy, (now, before) => {
  if (before && !now) modeOpen.value = modelOpen.value = false;
});
// A switch that failed says so once (the menu keeps the reason).
watch(
  () => c.value?.error,
  (err, before) => err && err !== before && toast.error(`切换没有成功：${err}`),
);

/** Digits pick a row while the mode menu is open (not while typing elsewhere). */
function onKey(e: KeyboardEvent) {
  if (!modeOpen.value || e.metaKey || e.ctrlKey || e.altKey) return;
  const row = rowForKey(rows.value, e.key);
  if (!row) return;
  e.preventDefault();
  e.stopPropagation();
  pickMode(row.id);
}
watch(modeOpen, (open) => (open ? window.addEventListener("keydown", onKey, true) : window.removeEventListener("keydown", onKey, true)));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

const chip = "inline-flex h-7 min-w-0 items-center gap-1.5 rounded-full border px-2.5 text-xs whitespace-nowrap";
const toneClass = computed(() => (busy.value && pendingMode.value ? TONE_CLASS.pending : TONE_CLASS[look.value.tone]));
</script>

<template>
  <span v-if="c" class="flex min-w-0 items-center gap-1.5" data-testid="session-controls">
    <template v-if="!narrow">
      <el-popover v-model:visible="modelOpen" trigger="click" placement="top-start" :width="330" popper-class="at-session-menu !p-1.5">
        <template #reference>
          <button type="button" :class="[chip, 'border-transparent bg-raised text-muted-foreground hover:text-foreground']" data-testid="model-control" title="模型 · 思考强度" aria-haspopup="menu" :aria-expanded="modelOpen">
            <Loader2 v-if="busy && !pendingMode" class="size-3 animate-spin" />
            <span class="truncate">{{ modelText }}</span>
            <ChevronDown class="size-3 shrink-0" />
          </button>
        </template>
        <ModelMenuBody :models="models" :model="c.model" :effort="c.effort" :efforts="c.node_outdated ? [] : efforts" :runtime="runtime" :disabled="busy" :note="modelNote" @model="pickModel" @effort="pickEffort" />
      </el-popover>
      <el-popover v-model:visible="modeOpen" trigger="click" placement="top-start" :width="360" popper-class="at-session-menu !p-1.5">
        <template #reference>
          <button
            type="button"
            :class="[chip, toneClass]"
            data-testid="mode-control"
            :data-mode="mode"
            :aria-busy="busy && Boolean(pendingMode)"
            :title="busy && pendingMode ? '已提交，等节点确认后才算切换成功' : `权限模式：${modeLabel(mode)}`"
            aria-haspopup="menu"
            :aria-expanded="modeOpen"
          >
            <template v-if="busy && pendingMode">
              <Loader2 class="size-3 animate-spin" />
              <span>切换到{{ modeLabel(pendingMode) }}…</span>
            </template>
            <template v-else>
              <ModeIcon :mode="mode" class="size-3" />
              <span>{{ look.state }}</span>
            </template>
            <ChevronDown class="size-3 shrink-0" />
          </button>
        </template>
        <ModeMenuBody :rows="rows" :current="mode" :pending="pendingMode" :runtime-label="runtimeLabel(runtime)" :notes="modeNotes" @pick="pickMode" />
      </el-popover>
    </template>
    <template v-else>
      <button type="button" :class="[chip, toneClass, 'max-w-full']" data-testid="controls-chip" @click="sheet = true">
        <Loader2 v-if="busy" class="size-3 shrink-0 animate-spin" />
        <ModeIcon v-else :mode="mode" class="size-3 shrink-0" />
        <span class="truncate">{{ modelText }} · {{ busy ? "切换中…" : look.short }}</span>
        <ChevronDown class="size-3 shrink-0" />
      </button>
      <el-drawer v-model="sheet" direction="btt" size="auto" title="模型和模式" append-to-body class="at-sheet !h-auto max-h-[85vh] rounded-t-2xl">
        <div class="flex flex-col gap-3 pb-2" data-testid="controls-sheet">
          <ModelMenuBody :models="models" :model="c.model" :effort="c.effort" :efforts="c.node_outdated ? [] : efforts" :runtime="runtime" :disabled="busy" :note="modelNote" touch @model="pickModel" @effort="pickEffort" />
          <div class="border-t pt-2">
            <ModeMenuBody :rows="rows" :current="mode" :pending="pendingMode" :runtime-label="runtimeLabel(runtime)" :notes="modeNotes" touch @pick="pickMode" />
          </div>
        </div>
      </el-drawer>
    </template>
  </span>
</template>
