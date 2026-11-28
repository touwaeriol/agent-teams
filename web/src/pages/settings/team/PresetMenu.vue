<script setup lang="ts">
import { $t } from "@/i18n";
/** 从预设添加: the platform's preset roles. */
import { ChevronDown } from "lucide-vue-next";
import { computed } from "vue";
import { useRolePresets } from "@/api/queries";
import type { RoleView } from "@/api/views";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import { roleName } from "@/lib/roles";

const emit = defineEmits<{ pick: [role: RoleView] }>();
const presets = useRolePresets();
const list = computed(() => presets.data.value?.roles ?? []);
const pick = (key: string) => {
  const p = list.value.find((r) => r.role === key);
  if (p) emit("pick", p);
};
</script>

<template>
  <DropdownMenu placement="bottom-end" popper-class="max-w-[320px]" @command="pick">
    <el-button class="h-[38px]" :disabled="list.length === 0">{{ $t("从预设添加") }} <ChevronDown /></el-button>
    <template #menu>
      <el-dropdown-item v-for="p in list" :key="p.role" :command="p.role" class="!flex-col !items-start !gap-0.5">
        <span class="font-medium">{{ roleName(p) }}</span>
        <span class="text-xs text-muted-foreground">{{ p.description }}</span>
      </el-dropdown-item>
    </template>
  </DropdownMenu>
</template>
