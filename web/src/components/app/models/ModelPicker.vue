<script setup lang="ts">
/**
 * A model field of one runtime: a text box for any model id (the server's format, MODEL_ID) with
 * suggestions — the models the roles of that runtime already use, then the common ids. Clearing
 * it saves "" (the placeholder says what "" means). There is no model list to manage.
 *
 * `live` (fields of a draft saved by a button: 团队岗位, 新建项目): every keystroke updates the
 * value (trimmed, even when the format is wrong: the form refuses to save then), so a button
 * clicked right after typing saves what is in the box. Otherwise (fields that save at once:
 * 项目设置) a typed id is committed on Enter, on picking a suggestion or at once when the box loses
 * focus (no delay, so closing the dialog right after typing keeps it); an id in the wrong format
 * is not committed. Either way a wrong format is marked.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoles } from "@/api/queries";
import { MODEL_FORMAT_HINT, modelName, modelSuggestions, validModel, type Runtime } from "@/lib/models";

const props = defineProps<{
  runtime: Runtime;
  label: string;
  /** What "" means, shown as the placeholder. */
  placeholder: string;
  size?: "default" | "small";
  /** Roles whose models are suggested; default the saved role templates. */
  roles?: { tier: string; runtime: string; model?: string }[];
  /** Update the value on every keystroke (draft forms). */
  live?: boolean;
}>();
const value = defineModel<string>({ required: true });
const saved = useRoles();
const draft = ref(value.value);
const bad = computed(() => !validModel(draft.value.trim()));
// The value coming back (or changed from outside): keep what is typed when it only differs in spaces.
watch(value, (v) => {
  if (draft.value.trim() !== v) draft.value = v;
});
watch(draft, (d) => {
  if (props.live && d.trim() !== value.value) value.value = d.trim();
});

const suggestions = computed(() => modelSuggestions(props.roles ?? saved.data.value?.roles ?? [], props.runtime));

type Item = { value: string; name: string };
function fetch(query: string, cb: (items: Item[]) => void) {
  const q = query.trim().toLowerCase();
  // The box's own value shows every suggestion (picking another is the common case).
  const all = suggestions.value.map((id) => ({ value: id, name: modelName(id) }));
  cb(!q || q === value.value.toLowerCase() ? all : all.filter((i) => i.value.toLowerCase().includes(q) || i.name.toLowerCase().includes(q)));
}

// Clicking a suggestion blurs the box first (mousedown → blur → click → select): remember that the
// mouse went down on the suggestions so the blur leaves the commit to the pick, not the partial text.
let picking = false;
const onDown = (e: MouseEvent) => (picking = Boolean((e.target as Element | null)?.closest?.(".at-model-picker")));
onMounted(() => document.addEventListener("mousedown", onDown, true));
onBeforeUnmount(() => document.removeEventListener("mousedown", onDown, true));
// Bound to the native focusout of the root, not el-autocomplete's blur: that one is emitted a tick
// later, when a dialog closed right after typing may already be gone.
function onBlur() {
  if (props.live) return void (draft.value = draft.value.trim());
  if (picking) return void (picking = false);
  commit();
}
function commit(v = draft.value) {
  picking = false;
  const id = v.trim();
  draft.value = id;
  if (!validModel(id) || id === value.value) return;
  value.value = id;
}
</script>

<template>
  <span class="flex min-w-0 flex-col gap-0.5" @focusout="onBlur">
    <el-autocomplete
      v-model="draft"
      :fetch-suggestions="fetch"
      :trigger-on-focus="true"
      :placeholder="placeholder"
      :aria-label="label"
      :size="size"
      :class="{ 'at-model-bad': bad }"
      clearable
      popper-class="at-model-picker"
      data-testid="model-input"
      @select="(i: Record<string, any>) => commit(i.value)"
      @keyup.enter="commit()"
      @clear="commit('')"
    >
      <template #default="{ item }">
        <span class="flex items-center justify-between gap-3">
          <span class="truncate">{{ item.value }}</span>
          <span v-if="item.name !== item.value" class="shrink-0 text-xs text-muted-foreground">{{ item.name }}</span>
        </span>
      </template>
    </el-autocomplete>
    <span v-if="bad" class="text-[11px] leading-4 text-[#B4542F]" data-testid="model-input-error">{{ MODEL_FORMAT_HINT }}</span>
  </span>
</template>

<style scoped>
.at-model-bad :deep(.el-input__wrapper) {
  box-shadow: 0 0 0 1px #b4542f inset;
}
</style>
