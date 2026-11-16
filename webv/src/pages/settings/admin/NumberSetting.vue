<script setup lang="ts">
/** A label and a small number box that saves on blur or Enter. */
import { ref, watch } from "vue";
import { parseSetting } from "./admin";

const props = defineProps<{ label: string; value: number }>();
const emit = defineEmits<{ save: [n: number] }>();
const text = ref(String(props.value));
watch(() => props.value, (v) => (text.value = String(v)));
function commit() {
  const n = parseSetting(text.value, props.value);
  if (n === null) text.value = String(props.value);
  else emit("save", n);
}
</script>

<template>
  <label class="flex items-center gap-2.5">
    {{ label }}
    <input v-model="text" inputmode="numeric" class="h-9 w-16 rounded-lg border border-input bg-transparent px-2.5" @blur="commit" @keydown.enter="commit" />
  </label>
</template>
