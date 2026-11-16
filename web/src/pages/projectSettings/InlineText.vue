<script setup lang="ts">
/** A text box that saves when it loses focus or on Enter (Escape puts the saved value back). */
import { ref, watch } from "vue";
import { cn } from "@/lib/utils";
import { inlineCommit } from "./logic";

const props = defineProps<{ value: string; label: string; placeholder?: string; bare?: boolean; required?: boolean }>();
const emit = defineEmits<{ save: [value: string] }>();
const text = ref(props.value);
watch(() => props.value, (v) => (text.value = v));
function commit() {
  const r = inlineCommit(text.value, props.value, Boolean(props.required));
  if (r.reset) text.value = props.value;
  if (r.save !== undefined) emit("save", r.save);
}
</script>

<template>
  <input
    v-model="text"
    :aria-label="label"
    :placeholder="placeholder"
    :class="cn('h-9 min-w-0 flex-1 bg-transparent px-2 outline-none', !bare && 'w-[min(300px,60vw)] rounded-lg bg-raised focus:ring-2 focus:ring-ring/40')"
    @blur="commit"
    @keydown.enter="($event.target as HTMLInputElement).blur()"
    @keydown.esc="text = value"
  />
</template>
