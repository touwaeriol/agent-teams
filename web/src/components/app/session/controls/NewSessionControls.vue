<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * The new-session page's thinking effort and permission mode (#53), from GET /api/session-options
 * for the picked role, agent (runtime) and model (the model chip sits before these in NewSessionOptions). The
 * picks go with POST /api/sessions; "" = the session's default (Claude: 自动 when the model
 * supports it, else 手动; Codex/Antigravity: the role's). The digits pick a mode while its menu
 * is open.
 */
import { ChevronDown, Gauge } from "lucide-vue-next";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useSessionOptions } from "@/api/queries";
import { runtimeLabel } from "@/lib/runtimes";
import { MODE_LOOK, TONE_CLASS, appliesNote, appliesNow, effortLabel, isMode, modeRows, noAutoNote, rowForKey } from "@/lib/sessionControls";
import ModeIcon from "./ModeIcon.vue";
import ModeMenuBody from "./ModeMenuBody.vue";

const props = defineProps<{ role: string; agent: string; model: string }>();
const mode = defineModel<string>("mode", { required: true });
const effort = defineModel<string>("effort", { required: true });
const opts = useSessionOptions(
  () => props.role,
  () => props.agent,
  () => props.model.trim(),
);
const o = computed(() => opts.data.value);
const rows = computed(() => modeRows(o.value?.modes));
const efforts = computed(() => o.value?.efforts ?? []);
const runtime = computed(() => o.value?.runtime ?? "claude");
const shown = computed(() => mode.value || o.value?.default_mode || "default");
const look = computed(() => (isMode(shown.value) ? MODE_LOOK[shown.value] : MODE_LOOK.default));
const effortText = computed(() => (effort.value ? effortLabel(effort.value) : o.value && efforts.value.length ? $t("默认强度") : ""));
// A pick the new role or model cannot take goes back to the default.
watch(o, (v) => {
  if (!v) return;
  if (mode.value && !rows.value.some((r) => r.id === mode.value)) mode.value = "";
  if (effort.value && !efforts.value.includes(effort.value)) effort.value = "";
});
const modeOpen = ref(false);
const effortOpen = ref(false);
const notes = computed(() => [
  noAutoNote(runtime.value, o.value?.models, o.value?.model ?? ""),
  $t("新会话默认：Claude 用“自动”（模型不支持时用“手动”），Codex 和 Antigravity 沿用岗位设置。会话里随时可以改。"),
  appliesNow(runtime.value, o.value?.modes) ? "" : appliesNote(runtime.value, o.value?.modes),
].filter(Boolean));
function pick(id: string) {
  mode.value = id === o.value?.default_mode ? "" : id;
  modeOpen.value = false;
}
function onKey(e: KeyboardEvent) {
  if (!modeOpen.value || e.metaKey || e.ctrlKey || e.altKey) return;
  const row = rowForKey(rows.value, e.key);
  if (!row) return;
  e.preventDefault();
  e.stopPropagation();
  pick(row.id);
}
watch(modeOpen, (open) => (open ? window.addEventListener("keydown", onKey, true) : window.removeEventListener("keydown", onKey, true)));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));
const chip = "inline-flex h-7 max-w-[240px] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground";
</script>

<template>
  <template v-if="o">
    <el-popover v-if="efforts.length" v-model:visible="effortOpen" trigger="click" placement="top-start" :width="260">
      <template #reference>
        <button type="button" :class="chip" data-testid="effort-chip" :title="$t('思考强度：{effortText}', { effortText })">
          <Gauge class="size-3.5 shrink-0" />
          <span class="truncate">{{ effortText }}</span>
          <ChevronDown class="size-3 shrink-0" />
        </button>
      </template>
      <div class="flex flex-col gap-2 text-[13px]">
        <span class="text-xs font-semibold">{{ $t("思考强度") }}</span>
        <div class="flex flex-wrap gap-1" data-testid="effort-chips">
          <button
            v-for="e in ['', ...efforts]"
            :key="e || 'default'"
            type="button"
            :aria-pressed="e === effort"
            :data-effort="e || 'default'"
            :class="['rounded-full border px-2.5 py-0.5 text-xs', e === effort ? 'border-foreground/60 bg-foreground text-background' : 'hover:bg-accent']"
            @click="(effort = e), (effortOpen = false)"
          >{{ e ? effortLabel(e) : $t("默认") }}</button>
        </div>
        <span class="text-[11.5px] text-muted-foreground">{{ $t("默认：项目或岗位设置的强度。") }}</span>
      </div>
    </el-popover>
    <el-popover v-if="rows.length" v-model:visible="modeOpen" trigger="click" placement="top-start" :width="360" popper-class="at-session-menu !p-1.5">
      <template #reference>
        <button type="button" :class="['inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs', TONE_CLASS[look.tone]]" data-testid="new-mode-chip" :data-mode="shown" :title="$t('权限模式：{state}', { state: look.state })">
          <ModeIcon :mode="shown" class="size-3" />
          <span>{{ look.state }}</span>
          <ChevronDown class="size-3 shrink-0" />
        </button>
      </template>
      <ModeMenuBody :rows="rows" :current="shown" :runtime-label="runtimeLabel(runtime)" :notes="notes" @pick="pick" />
    </el-popover>
  </template>
</template>
