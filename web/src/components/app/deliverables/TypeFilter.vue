<script setup lang="ts">
import { $t } from "@/i18n";
import { ChevronDown } from "lucide-vue-next";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import { cn } from "@/lib/utils";
import { LIBRARY_TYPES, typeName } from "./browser";

defineProps<{ value: string }>();
const emit = defineEmits<{ change: [value: string] }>();
</script>

<template>
  <DropdownMenu @command="(c: string) => emit('change', c)">
    <button type="button" class="flex h-8 items-center gap-1 rounded-lg px-2 text-[13px] font-medium hover:bg-raised aria-expanded:bg-raised">
      {{ value ? typeName(value) : $t("类型") }}
      <ChevronDown class="size-3.5 text-muted-foreground" />
    </button>
    <template #menu>
      <el-dropdown-item v-for="[id, name] in LIBRARY_TYPES" :key="id || 'all'" :command="id" :class="cn(id === value && 'font-semibold')">{{ name }}</el-dropdown-item>
    </template>
  </DropdownMenu>
</template>
