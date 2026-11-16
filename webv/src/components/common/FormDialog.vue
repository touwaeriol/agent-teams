<script setup lang="ts">
/** A modal form: title, fields, one submit button that shows progress. `v-model:open`. */
import { ref } from "vue";
import { cn } from "@/lib/utils";
import Spinner from "./Spinner.vue";

withDefaults(defineProps<{ title: string; description?: string; submitLabel: string; busy?: boolean; disabled?: boolean; class?: string; width?: string }>(), {
  description: undefined,
  class: undefined,
  width: "32rem",
});
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ submit: [] }>();
const form = ref<HTMLFormElement | null>(null);
/** Like Radix: the first field takes focus when the dialog opens. */
const focusFirst = () => form.value?.querySelector<HTMLElement>("input, textarea, select")?.focus();
</script>

<template>
  <el-dialog align-center v-model="open" :show-close="true" :class="cn('at-dialog w-[calc(100%-2rem)] max-w-lg', $props.class)" :width="width" append-to-body destroy-on-close @opened="focusFirst">
    <template #header>
      <div class="flex flex-col gap-1.5 pr-6">
        <h2 class="text-lg font-semibold">{{ title }}</h2>
        <p v-if="description || $slots.description" class="text-[13px] leading-5 text-muted-foreground"><slot name="description">{{ description }}</slot></p>
      </div>
    </template>
    <form ref="form" class="flex flex-col gap-4" @submit.prevent="emit('submit')">
      <slot />
      <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <el-button type="primary" native-type="submit" :disabled="disabled || busy">
          <Spinner v-if="busy" />
          {{ submitLabel }}
        </el-button>
      </div>
    </form>
  </el-dialog>
</template>
