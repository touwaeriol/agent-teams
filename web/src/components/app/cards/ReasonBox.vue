<script setup lang="ts">
/** A reason textarea with confirm / cancel (rejections, change requests). */
import { ref } from "vue";

defineProps<{ placeholder: string; confirm: string; busy?: boolean }>();
const emit = defineEmits<{ submit: [text: string]; cancel: [] }>();
const text = ref("");
const vFocus = { mounted: (el: HTMLElement) => el.focus() };
</script>

<template>
  <form class="flex flex-col gap-2" @submit.prevent="emit('submit', text.trim())">
    <textarea
      v-model="text"
      v-focus
      rows="2"
      :placeholder="placeholder"
      class="w-full resize-none rounded-lg border border-input bg-white px-2.5 py-2 text-[13px] outline-none focus:ring-2 focus:ring-ring/40"
    />
    <span class="flex gap-2">
      <button type="submit" :disabled="busy" class="h-8 rounded-lg bg-primary px-3 text-[13px] text-primary-foreground disabled:opacity-60">{{ confirm }}</button>
      <button type="button" class="h-8 rounded-lg border border-input bg-white px-3 text-[13px]" @click="emit('cancel')">取消</button>
    </span>
  </form>
</template>
