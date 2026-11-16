<script setup lang="ts">
// Free-form list of short strings (the React TagInput): Enter or a comma adds, Backspace on an
// empty box removes the last, leaving the box keeps what was typed. `v-model` is the list.
import { cn } from "@/lib/utils";

defineProps<{ placeholder?: string; id?: string; class?: string }>();
const values = defineModel<string[]>({ required: true });
/** Items are trimmed and kept once, in the order first typed. */
const set = (next: string[] | undefined) => (values.value = [...new Set((next ?? []).map((v) => v.trim()).filter(Boolean))]);
</script>

<template>
  <el-input-tag
    :id="id"
    :model-value="values"
    :class="cn('at-tag-input', $props.class)"
    :placeholder="placeholder"
    :delimiter="/[,，]/"
    trigger="Enter"
    save-on-blur
    tag-effect="plain"
    @update:model-value="set"
  />
</template>
