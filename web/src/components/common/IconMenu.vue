<script setup lang="ts">
// An IconButton that opens a dropdown menu: the tooltip wraps the dropdown (a tooltip cannot be
// the dropdown's trigger itself) and stays quiet while the menu is open. Items go in the `menu`
// slot as <el-dropdown-item command="…">; the chosen command is emitted.
import { ref } from "vue";
import { cn } from "@/lib/utils";
import DropdownMenu from "./DropdownMenu.vue";

withDefaults(defineProps<{ label: string; class?: string; placement?: "bottom-end" | "bottom-start"; popperClass?: string }>(), {
  placement: "bottom-end",
  class: undefined,
  popperClass: "w-44",
});
const emit = defineEmits<{ command: [command: string] }>();
const open = ref(false);
const menu = ref<InstanceType<typeof DropdownMenu> | null>(null);
defineExpose({ close: () => menu.value?.close() });
const ICON =
  "flex size-8 shrink-0 items-center justify-center rounded-lg text-[#4A4843] transition-colors hover:bg-raised dark:text-foreground/80 [&_svg]:size-[17px] aria-expanded:bg-raised";
</script>

<template>
  <el-tooltip :content="label" placement="bottom" :show-after="300" :hide-after="0" effect="dark" :show-arrow="false" :offset="6" :disabled="open">
    <span :class="cn('inline-flex', $props.class)">
      <DropdownMenu ref="menu" :placement="placement" :popper-class="popperClass" @command="(c: string) => emit('command', c)" @open="(v: boolean) => (open = v)">
        <button type="button" :aria-label="label" :class="ICON"><slot /></button>
        <template #menu><slot name="menu" /></template>
      </DropdownMenu>
    </span>
  </el-tooltip>
</template>
