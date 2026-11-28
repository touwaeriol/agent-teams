<script setup lang="ts">
// 项目设置 as a dialog with a section nav (通用 / 记忆 / 环境 / 使用情况 / 高级).
// /projects/:pid/settings[/:section] opens it over the main board. Switches and selects save at
// once; text saves on blur (name, intro) or with its button (项目指令, memory files).
import { $t } from "@/i18n";
import { BarChart3, Brain, FolderGit2, Settings2, Wrench } from "lucide-vue-next";
import type { Component } from "vue";
import type { ProjectView } from "@/api/views";
import { SETTINGS_SECTIONS, type SettingsSection } from "@/lib/projectSettings";
import { cn } from "@/lib/utils";
import AdvancedSection from "./AdvancedSection.vue";
import EnvironmentSection from "./EnvironmentSection.vue";
import GeneralSection from "./GeneralSection.vue";
import MemorySection from "./MemorySection.vue";
import UsageSection from "./UsageSection.vue";

defineProps<{ project: ProjectView; section: SettingsSection }>();
const emit = defineEmits<{ section: [section: SettingsSection]; close: [] }>();
const ICONS: Record<SettingsSection, Component> = { general: Settings2, memory: Brain, environment: FolderGit2, usage: BarChart3, advanced: Wrench };
const close = (o: boolean) => !o && emit("close");
</script>

<template>
  <el-dialog
    :model-value="true"
    align-center
    append-to-body
    class="at-dialog at-dialog-flush flex h-[min(800px,92vh)] max-h-[90vh] w-[calc(100%-2rem)] max-w-[960px] flex-col overflow-hidden !rounded-[14px]"
    width="960px"
    :aria-label="$t('{name} 的设置', { name: project.name })"
    @update:model-value="close"
  >
    <template #header>
      <div class="flex h-14 shrink-0 items-center border-b px-5">
        <h2 class="text-[15px] font-medium">{{ $t("项目设置") }}</h2>
        <span class="sr-only">{{ $t("{name} 的设置", { name: project.name }) }}</span>
      </div>
    </template>
    <div class="flex h-full min-h-0 min-w-0 flex-1 max-md:flex-col">
      <nav :aria-label="$t('设置分区')" class="scroll-thin flex shrink-0 gap-0.5 overflow-x-auto border-r p-3 max-md:border-b max-md:border-r-0 md:w-[200px] md:flex-col">
        <button
          v-for="s in SETTINGS_SECTIONS"
          :key="s.id"
          type="button"
          :aria-current="section === s.id ? 'page' : undefined"
          :class="
            cn(
              'flex h-9 shrink-0 items-center gap-2.5 rounded-lg px-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-4 [&_svg]:shrink-0',
              section === s.id ? 'bg-accent font-medium' : 'text-foreground/80 hover:bg-raised',
            )
          "
          @click="emit('section', s.id)"
        >
          <component :is="ICONS[s.id]" />
          {{ s.label }}
        </button>
      </nav>
      <div class="scroll-thin min-h-0 min-w-0 flex-1 overflow-y-auto overflow-x-hidden px-5 pb-8 pt-4 [overflow-wrap:anywhere]">
        <MemorySection v-if="section === 'memory'" :project="project" />
        <EnvironmentSection v-else-if="section === 'environment'" :project="project" />
        <UsageSection v-else-if="section === 'usage'" :pid="project.id" />
        <AdvancedSection v-else-if="section === 'advanced'" :project="project" />
        <GeneralSection v-else :project="project" @close="emit('close')" />
      </div>
    </div>
  </el-dialog>
</template>
