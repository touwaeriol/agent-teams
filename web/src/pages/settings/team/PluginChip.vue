<script setup lang="ts">
import { $t } from "@/i18n";
/** A plugin to tick for a role; once ticked, a version picker (latest, or pin one). */
import { Check } from "lucide-vue-next";
import { computed } from "vue";
import type { PluginView } from "@/api/views";
import { resolvedVersion, type PluginRef } from "@/lib/plugins";
import { cn } from "@/lib/utils";
import ToggleChip from "./ToggleChip.vue";
import { versionText } from "./team";

const props = defineProps<{ plugin: PluginView; entry?: PluginRef }>();
const emit = defineEmits<{ toggle: []; pin: [version: number] }>();
const gone = computed(() => Boolean(props.entry) && resolvedVersion(props.plugin, props.entry!) === undefined);
const label = computed(() => $t("{name}{n}{n2}", { name: props.plugin.name, n: props.plugin.builtin ? $t(" · 内置") : "", n2: props.plugin.enabled ? "" : $t(" · 已停用") }));
</script>

<template>
  <span class="inline-flex items-center gap-1" :title="plugin.versions?.[0]?.description || undefined">
    <ToggleChip :on="Boolean(entry)" @click="emit('toggle')"><Check v-if="entry" class="mr-0.5 inline size-3" />{{ label }}</ToggleChip>
    <el-select
      v-if="entry"
      :model-value="String(entry.version)"
      :aria-label="$t('{name} 的版本', { name: plugin.name })"
      :class="cn('at-select-28 at-select-fit', gone && 'at-select-bad')"
      popper-class="at-select-popper"
      @update:model-value="(v: string) => emit('pin', Number(v))"
    >
      <template #label>{{ versionText(entry.version, plugin.latest, gone) }}</template>
      <el-option value="0" :label="$t('最新（v{latest}）', { latest: plugin.latest })" />
      <el-option v-for="v in plugin.versions ?? []" :key="v.version" :value="String(v.version)" :label="`v${v.version}${v.manifest_version ? ` · ${v.manifest_version}` : ''}`" />
    </el-select>
  </span>
</template>
