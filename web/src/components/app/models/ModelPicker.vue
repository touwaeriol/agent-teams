<script setup lang="ts">
/**
 * A model field of one runtime: a text box for any model id (the server's format, MODEL_ID) with
 * suggestions — the models the roles of that runtime already use, then the common ids. Clearing
 * it saves "" (the placeholder says what "" means). A typed id is committed on Enter, on picking a
 * suggestion or when the box loses focus; an id in the wrong format is not committed and is
 * marked. There is no model list to manage.
 */
import { computed, ref, watch } from "vue";
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
}>();
const value = defineModel<string>({ required: true });
const saved = useRoles();
const draft = ref(value.value);
const bad = computed(() => !validModel(draft.value.trim()));
watch(value, (v) => (draft.value = v));

const suggestions = computed(() => modelSuggestions(props.roles ?? saved.data.value?.roles ?? [], props.runtime));

type Item = { value: string; name: string };
function fetch(query: string, cb: (items: Item[]) => void) {
  const q = query.trim().toLowerCase();
  // The box's own value shows every suggestion (picking another is the common case).
  const all = suggestions.value.map((id) => ({ value: id, name: modelName(id) }));
  cb(!q || q === value.value.toLowerCase() ? all : all.filter((i) => i.value.toLowerCase().includes(q) || i.name.toLowerCase().includes(q)));
}

// Clicking a suggestion blurs the box first: wait a moment so the pick, not the partial text, is committed.
let blurTimer: ReturnType<typeof setTimeout> | undefined;
function onBlur() {
  clearTimeout(blurTimer);
  blurTimer = setTimeout(() => commit(), 150);
}
function commit(v = draft.value) {
  clearTimeout(blurTimer);
  const id = v.trim();
  draft.value = id;
  if (!validModel(id) || id === value.value) return;
  value.value = id;
}
</script>

<template>
  <span class="flex min-w-0 flex-col gap-0.5">
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
      @blur="onBlur"
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
