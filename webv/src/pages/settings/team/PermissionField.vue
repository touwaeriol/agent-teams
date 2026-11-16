<script setup lang="ts">
/** 权限: 直接执行 (bypass, default) / 逐条问 (ask), with one line on what it does. */
import { computed } from "vue";
import type { RoleView } from "@/api/views";
import { PERMISSION_MODES, permissionHint, permissionMode, type PermissionMode } from "@/lib/roles";

const props = defineProps<{ role: RoleView }>();
const emit = defineEmits<{ change: [patch: Partial<RoleView>] }>();
const id = computed(() => `perm-${props.role.id || props.role.role}`);
</script>

<template>
  <div class="flex flex-col gap-1.5 border-t border-[#F1EFEA] pt-2.5">
    <span class="flex items-center justify-between gap-3">
      <label :for="id" class="font-medium">权限</label>
      <el-select :id="id" :model-value="permissionMode(role)" class="at-select-32 !w-[168px]" @update:model-value="(v: PermissionMode) => emit('change', { permission_mode: v })">
        <el-option v-for="m in PERMISSION_MODES" :key="m.id" :value="m.id" :label="m.label" />
      </el-select>
    </span>
    <span class="text-xs leading-[18px] text-muted-foreground">{{ permissionHint(role) }}</span>
  </div>
</template>
