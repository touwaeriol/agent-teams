<script setup lang="ts">
// A labelled form control. The default slot receives `{ id }`: bind it to the control so the
// label targets it (`<Field label="名称" v-slot="{ id }"><el-input :id="id" /></Field>`).
import { computed, provide, useId } from "vue";
import { cn } from "@/lib/utils";

const props = defineProps<{ label?: string; hint?: string; for?: string; class?: string }>();
const generated = useId();
const id = computed(() => props.for ?? generated);
provide("field-id", id);
</script>

<template>
  <div :class="cn('flex flex-col gap-1.5', props.class)">
    <label :for="id" class="text-[13px] font-medium text-foreground/85"><slot name="label">{{ label }}</slot></label>
    <slot :id="id" />
    <p v-if="hint || $slots.hint" class="text-xs leading-5 text-muted-foreground"><slot name="hint">{{ hint }}</slot></p>
  </div>
</template>
