<script setup lang="ts">
import { $t } from "@/i18n";
/** The project's icon (emoji, initial colour, or default) and its name, saved on blur. */
import type { ProjectView } from "@/api/views";
import ProjectIcon from "@/components/app/ProjectIcon.vue";
import { colorIcon, ICON_COLORS, ICON_EMOJIS } from "@/lib/projectIcon";
import InlineText from "./InlineText.vue";
import { useUpdate } from "./useUpdate";

const props = defineProps<{ project: ProjectView }>();
const update = useUpdate(() => props.project.id);
const popperOptions = { modifiers: [{ name: "offset", options: { offset: [0, 6] } }] };
const saveName = (name: string) => name.trim() && update.mutate({ name: name.trim() });
</script>

<template>
  <span class="flex h-9 w-[min(300px,60vw)] items-center gap-1 rounded-lg bg-raised pl-1 pr-1">
    <el-dropdown trigger="click" placement="bottom-start" popper-class="at-menu w-64" :show-arrow="false" :popper-options="popperOptions" @command="(icon: string) => update.mutate({ icon })">
      <button type="button" :aria-label="$t('更换图标')" class="flex size-7 shrink-0 items-center justify-center rounded-md hover:bg-background">
        <ProjectIcon :project="project" :size="22" />
      </button>
      <template #dropdown>
        <el-dropdown-menu class="!p-2">
          <div class="px-2 py-1.5 text-xs text-muted-foreground">{{ $t("表情") }}</div>
          <div class="grid grid-cols-8 gap-0.5 px-1">
            <el-dropdown-item v-for="e in ICON_EMOJIS" :key="e" :command="e" :aria-label="$t('图标 {e}', { e })" class="!justify-center !p-1 !text-lg">{{ e }}</el-dropdown-item>
          </div>
          <div class="px-2 py-1.5 text-xs text-muted-foreground">{{ $t("首字母配色") }}</div>
          <div class="flex flex-wrap gap-1.5 px-2 pb-1">
            <el-dropdown-item v-for="c in ICON_COLORS" :key="c" :command="colorIcon(c)" :aria-label="$t('颜色 {c}', { c })" class="size-6 !rounded-full !p-0" :style="{ background: c }" />
          </div>
          <el-dropdown-item command="" class="mt-1 !text-[13px] !text-muted-foreground">{{ $t("恢复默认") }}</el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <InlineText :value="project.name" :label="$t('项目名称')" bare required @save="saveName" />
  </span>
</template>
