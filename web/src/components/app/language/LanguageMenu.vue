<script setup lang="ts">
// Sidebar quick switch for the interface language (same choice as 设置 → 外观与语言).
import { Languages } from "lucide-vue-next";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import { $t, storedChoice } from "@/i18n";
import { cn } from "@/lib/utils";
import { languageOptions, useSaveLanguage } from "./language";

const options = languageOptions();
const choice = storedChoice();
const save = useSaveLanguage();
const pick = (v: string) => {
  if (v !== choice && !save.isPending.value) save.mutate(v);
};
</script>

<template>
  <DropdownMenu placement="top-end" @command="pick">
    <button type="button" :aria-label="$t('界面语言')" :title="$t('界面语言')" class="flex size-8 items-center justify-center rounded-lg text-[#4A4843] hover:bg-accent aria-expanded:bg-accent">
      <Languages class="size-4" :stroke-width="1.8" />
    </button>
    <template #menu>
      <el-dropdown-item v-for="o in options" :key="o.value || 'auto'" :command="o.value" :class="cn(o.value === choice && 'font-semibold')">{{ o.label }}</el-dropdown-item>
    </template>
  </DropdownMenu>
</template>
