<script setup lang="ts">
/** Which nodes a role may run on: a multi-pick menu that stays open; empty = any node. */
import { Check, ChevronDown } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useNodes } from "@/api/queries";
import { nodePickerText, toggleId } from "./team";

const value = defineModel<string[]>({ required: true });
const nodes = useNodes();
const list = computed(() => nodes.data.value ?? []);
const text = computed(() => nodePickerText(value.value, list.value));
const menu = ref<{ handleClose: () => void } | null>(null);
const popperOptions = { modifiers: [{ name: "offset", options: { offset: [0, 6] } }] };

function command(c: string) {
  if (c !== "") return void (value.value = toggleId(value.value, c));
  value.value = [];
  menu.value?.handleClose();
}
</script>

<template>
  <el-dropdown ref="menu" trigger="click" placement="bottom-start" popper-class="at-menu" :show-arrow="false" :hide-on-click="false" :popper-options="popperOptions" @command="command">
    <button type="button" class="flex h-9 w-full items-center justify-between gap-2 rounded-lg border border-input bg-well px-2.5 text-left">
      <span class="truncate">{{ text }}</span>
      <ChevronDown class="size-4 shrink-0 text-muted-foreground" />
    </button>
    <template #dropdown>
      <el-dropdown-menu>
        <div v-if="list.length === 0" class="px-2 py-1.5 text-xs text-muted-foreground">还没有节点</div>
        <el-dropdown-item v-for="n in list" :key="n.id" :command="n.id">
          <span class="flex size-4 items-center justify-center"><Check v-if="value.includes(n.id)" /></span>
          <span class="font-mono">{{ n.name }}</span>
          <span class="ml-auto pl-4 text-xs text-muted-foreground">{{ n.online ? "在线" : "离线" }}</span>
        </el-dropdown-item>
        <el-dropdown-item v-if="value.length > 0" command="">不限节点</el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>
