<script setup lang="ts">
/** A reason textarea with confirm / cancel (rejections, change requests). The text stays while busy and after a failed send. */
import { computed, ref } from "vue";
import Spinner from "@/components/common/Spinner.vue";

const props = defineProps<{ placeholder: string; confirm: string; busy?: boolean; required?: boolean }>();
const emit = defineEmits<{ submit: [text: string]; cancel: [] }>();
const text = ref("");
const blocked = computed(() => props.busy || (props.required && !text.value.trim()));
const vFocus = { mounted: (el: HTMLElement) => el.focus() };
const submit = () => !blocked.value && emit("submit", text.value.trim());
</script>

<template>
  <form class="flex flex-col gap-2" :aria-busy="busy || undefined" @submit.prevent="submit">
    <textarea
      v-model="text"
      v-focus
      rows="2"
      :readonly="busy"
      :placeholder="placeholder"
      class="w-full resize-none rounded-lg border border-input bg-white px-2.5 py-2 text-[13px] outline-none focus:ring-2 focus:ring-ring/40 read-only:opacity-60"
    />
    <span class="flex gap-2">
      <button type="submit" :disabled="blocked" class="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3 text-[13px] text-primary-foreground disabled:opacity-60">
        <Spinner v-if="busy" class="size-3.5" decorative />{{ confirm }}
      </button>
      <button type="button" :disabled="busy" class="h-8 rounded-lg border border-input bg-white px-3 text-[13px] disabled:opacity-60" @click="emit('cancel')">取消</button>
    </span>
  </form>
</template>
