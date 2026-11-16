<script setup lang="ts">
// Every page: centered container, header, then its content in a column.
import { cn } from "@/lib/utils";
import PageHeader from "./PageHeader.vue";

const WIDTH = { full: "max-w-6xl", narrow: "max-w-3xl" };
withDefaults(defineProps<{ title?: string; description?: string; width?: keyof typeof WIDTH }>(), { width: "full" });
</script>

<template>
  <div :class="cn('mx-auto flex w-full flex-col gap-5 px-5 py-8 sm:px-8', WIDTH[width])">
    <slot name="before" />
    <PageHeader v-if="title || $slots.title" :title="title" :description="description">
      <template v-if="$slots.title" #title><slot name="title" /></template>
      <template v-if="$slots.description" #description><slot name="description" /></template>
      <template v-if="$slots.actions" #actions><slot name="actions" /></template>
    </PageHeader>
    <slot />
  </div>
</template>
