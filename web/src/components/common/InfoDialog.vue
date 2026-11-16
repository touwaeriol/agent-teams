<script setup lang="ts">
/** A dialog that only shows information, closed with one button. */
import { cn } from "@/lib/utils";

defineProps<{ title: string; description?: string; class?: string }>();
const open = defineModel<boolean>("open", { required: true });
</script>

<template>
  <el-dialog align-center v-model="open" :class="cn('at-dialog w-[calc(100%-2rem)] max-w-lg', $props.class)" width="32rem" append-to-body>
    <template #header>
      <div class="flex flex-col gap-1.5 pr-6">
        <h2 class="text-lg font-semibold">{{ title }}</h2>
        <p v-if="description" class="text-[13px] leading-5 text-muted-foreground">{{ description }}</p>
      </div>
    </template>
    <slot />
    <template #footer>
      <el-button type="primary" @click="open = false">完成</el-button>
    </template>
  </el-dialog>
</template>
