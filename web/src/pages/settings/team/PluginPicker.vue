<script setup lang="ts">
import { $t } from "@/i18n";
/** Plugins this role loads: tick a plugin (latest version), then optionally pin a version. */
import { computed } from "vue";
import { usePlugins } from "@/api/queries";
import type { RoleView } from "@/api/views";
import { missingRefs, pinPlugin, refFor, runtimeNote, splitLibrary, togglePlugin } from "@/lib/plugins";
import { roleName } from "@/lib/roles";
import PluginChip from "./PluginChip.vue";
import ToggleChip from "./ToggleChip.vue";

const props = defineProps<{ role: RoleView }>();
const emit = defineEmits<{ change: [plugins: string[]] }>();
const plugins = usePlugins();
const all = computed(() => plugins.data.value?.plugins ?? []);
const ordered = computed(() => {
  const { mine, builtin } = splitLibrary(all.value);
  return [...mine, ...builtin];
});
const value = computed(() => props.role.plugins ?? []);
const note = computed(() => (value.value.length > 0 ? runtimeNote(props.role) : ""));
</script>

<template>
  <div class="flex flex-col gap-1.5 border-t border-[#F1EFEA] pt-2.5 text-[13px]" :aria-label="$t('{role} 的插件', { role: roleName(role) })">
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="mr-1 text-xs text-muted-foreground">{{ $t("插件") }}</span>
      <PluginChip
        v-for="p in ordered"
        :key="p.id"
        :plugin="p"
        :entry="refFor(value, p.name)"
        @toggle="emit('change', togglePlugin(value, p.name))"
        @pin="(v: number) => emit('change', pinPlugin(value, p.name, v))"
      />
      <ToggleChip v-for="r in missingRefs(value, all)" :key="r.name" on @click="emit('change', togglePlugin(value, r.name))">{{ $t("{name}（已删除）", { name: r.name }) }}</ToggleChip>
      <span v-if="all.length === 0" class="text-xs text-muted-foreground">{{ $t("插件库是空的") }}</span>
    </div>
    <span v-if="note" class="text-xs leading-[18px] text-muted-foreground">{{ note }}</span>
  </div>
</template>
