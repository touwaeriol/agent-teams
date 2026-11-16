<script setup lang="ts" generic="O extends { value: string }">
// A select you can type into (the React Combobox): the list filters as you type (`filter`, default
// "value contains the text"), arrows and Enter pick, and with `allow-custom` whatever was typed can
// be used as is. Emits `pick` with the chosen value; rich rows go in the `option` / `hint` slots.
import { ChevronsUpDown } from "lucide-vue-next";
import { computed, ref } from "vue";
import { cn } from "@/lib/utils";

const props = withDefaults(
  defineProps<{
    options: O[];
    modelValue?: string;
    filter?: (option: O, text: string) => boolean;
    placeholder?: string;
    loading?: boolean;
    empty?: string;
    allowCustom?: boolean;
    disabled?: boolean;
    id?: string;
    class?: string;
  }>(),
  { modelValue: "", filter: undefined, placeholder: undefined, empty: "没有匹配的选项", id: undefined, class: undefined },
);
const emit = defineEmits<{ pick: [value: string] }>();
const text = ref("");
const contains = (o: O, t: string) => o.value.toLowerCase().includes(t.trim().toLowerCase());
const shown = computed(() => props.options.filter((o) => (props.filter ?? contains)(o, text.value)).slice(0, 100));
const noMatch = computed(() => (props.allowCustom && text.value.trim() ? `按 Enter 使用“${text.value.trim()}”` : props.empty));

function pick(v: unknown) {
  const value = String(v ?? "").trim();
  text.value = "";
  if (value) emit("pick", value);
}
</script>

<template>
  <el-select
    :id="id"
    :model-value="modelValue || undefined"
    :class="cn('at-combobox w-full', props.class)"
    popper-class="at-combobox-popper"
    filterable
    :allow-create="allowCustom"
    default-first-option
    :filter-method="(t: string) => (text = t)"
    :loading="loading"
    loading-text="加载中……"
    :no-match-text="noMatch"
    :no-data-text="noMatch"
    :placeholder="placeholder"
    :disabled="disabled"
    :teleported="true"
    :show-arrow="false"
    :suffix-icon="ChevronsUpDown"
    :offset="4"
    @change="pick"
  >
    <el-option v-for="o in shown" :key="o.value" :value="o.value" :label="o.value">
      <span class="flex items-center gap-2">
        <span class="min-w-0 flex-1 truncate"><slot name="option" :option="o">{{ o.value }}</slot></span>
        <span v-if="$slots.hint" class="flex shrink-0 items-center text-xs text-muted-foreground"><slot name="hint" :option="o" /></span>
      </span>
    </el-option>
  </el-select>
</template>
