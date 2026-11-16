<script setup lang="ts">
/** Enable switch + delete for a plugin the viewer manages (their own, or built-ins on Admin). */
import type { PluginView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";

defineProps<{ plugin: PluginView }>();
const emit = defineEmits<{ toggle: [on: boolean]; remove: [] }>();
</script>

<template>
  <span class="flex shrink-0 items-center gap-1.5">
    <el-switch
      :model-value="plugin.enabled"
      :aria-label="`${plugin.enabled ? '停用' : '启用'}插件 ${plugin.name}`"
      @change="(v: string | number | boolean) => emit('toggle', Boolean(v))"
    />
    <ConfirmAction
      :icon-only="`删除插件 ${plugin.name}`"
      :title="`删除插件 ${plugin.name}？`"
      description="所有版本都会删除，用到它的岗位会去掉这个插件，已经开工的员工下次开会话时生效。"
      @confirm="emit('remove')"
    />
  </span>
</template>
