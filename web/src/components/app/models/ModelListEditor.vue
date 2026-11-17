<script setup lang="ts">
/**
 * The user's editable model list: type, model id, display name, order and one default per type.
 * Edits a draft; 保存 hands the whole list back (PUT /api/models/mine replaces it).
 */
import { ArrowDown, ArrowUp, X } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import Spinner from "@/components/common/Spinner.vue";
import { modelProblems, type ModelEntry } from "@/lib/models";

const props = defineProps<{ rows: ModelEntry[]; busy?: boolean }>();
const emit = defineEmits<{ save: [list: ModelEntry[]] }>();
const draft = ref<ModelEntry[]>([]);
watch(() => props.rows, (r) => (draft.value = r.map((m) => ({ ...m }))), { immediate: true });
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(props.rows));
const problems = computed(() => modelProblems(draft.value.map((m) => ({ ...m, model: m.model.trim() }))));
const add = () => (draft.value = [...draft.value, { id: "", runtime: "claude", model: "", label: "", default: false, mine: true }]);
const remove = (i: number) => (draft.value = draft.value.filter((_, j) => j !== i));
function move(i: number, d: number) {
  const next = [...draft.value];
  const [m] = next.splice(i, 1);
  next.splice(i + d, 0, m!);
  draft.value = next;
}
function setDefault(i: number, on: boolean) {
  const rt = draft.value[i]!.runtime;
  draft.value = draft.value.map((m, j) => (j === i ? { ...m, default: on } : on && m.runtime === rt ? { ...m, default: false } : m));
}
const save = () => emit("save", draft.value.map((m) => ({ ...m, model: m.model.trim(), label: (m.label ?? "").trim() })));
</script>

<template>
  <div class="flex flex-col gap-2" data-testid="model-list-editor">
    <div v-for="(m, i) in draft" :key="i" class="flex flex-wrap items-center gap-2">
      <el-select v-model="m.runtime" size="small" class="!w-[120px]" aria-label="类型">
        <el-option value="claude" label="Claude Code" />
        <el-option value="codex" label="Codex" />
      </el-select>
      <el-input v-model="m.model" size="small" class="at-mono !w-[220px]" placeholder="模型 ID" aria-label="模型 ID" />
      <el-input v-model="m.label" size="small" class="!w-[140px]" placeholder="显示名（可选）" aria-label="显示名" />
      <label class="flex items-center gap-1 text-xs text-muted-foreground">
        <el-checkbox :model-value="Boolean(m.default)" size="small" @update:model-value="(v: string | number | boolean) => setDefault(i, Boolean(v))" />我的默认
      </label>
      <span class="flex">
        <button type="button" aria-label="上移" :disabled="i === 0" class="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-raised disabled:opacity-30" @click="move(i, -1)"><ArrowUp class="size-3.5" /></button>
        <button type="button" aria-label="下移" :disabled="i === draft.length - 1" class="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-raised disabled:opacity-30" @click="move(i, 1)"><ArrowDown class="size-3.5" /></button>
        <button type="button" aria-label="删除" class="flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-raised" @click="remove(i)"><X class="size-3.5" /></button>
      </span>
    </div>
    <p v-if="draft.length === 0" class="text-xs text-muted-foreground">还没有模型，至少要留一个。</p>
    <p v-if="dirty && problems.length" class="text-xs text-destructive">{{ problems.join("；") }}</p>
    <div class="flex gap-2">
      <el-button size="small" @click="add">添加模型</el-button>
      <el-button size="small" type="primary" :disabled="!dirty || problems.length > 0 || busy" @click="save"><Spinner v-if="busy" />保存</el-button>
    </div>
  </div>
</template>
