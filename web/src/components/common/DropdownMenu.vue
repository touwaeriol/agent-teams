<script setup lang="ts">
// The app's dropdown (Radix DropdownMenu look): opens on click, no arrow, 6px from the trigger.
// Default slot = trigger; `menu` slot = <el-dropdown-item command="…"> items (or custom content
// with `bare`). Emits the chosen command and open/close.
import { ref } from "vue";

withDefaults(defineProps<{ placement?: "bottom-end" | "bottom-start" | "top-start" | "top-end"; popperClass?: string; bare?: boolean }>(), {
  placement: "bottom-start",
  popperClass: "",
});
const emit = defineEmits<{ command: [command: string]; open: [open: boolean] }>();
const popperOptions = { modifiers: [{ name: "offset", options: { offset: [0, 6] } }] };
const dropdown = ref<{ handleClose: () => void } | null>(null);
/** Closes the menu (a submenu's pick does not close its parent by itself). */
defineExpose({ close: () => dropdown.value?.handleClose() });
</script>

<template>
  <el-dropdown
    ref="dropdown"
    trigger="click"
    :placement="placement"
    :popper-class="`at-menu ${popperClass}`"
    :show-arrow="false"
    :popper-options="popperOptions"
    @command="(c: string) => emit('command', c)"
    @visible-change="(v: boolean) => emit('open', v)"
  >
    <slot />
    <template #dropdown>
      <slot v-if="bare" name="menu" />
      <el-dropdown-menu v-else><slot name="menu" /></el-dropdown-menu>
    </template>
  </el-dropdown>
</template>
