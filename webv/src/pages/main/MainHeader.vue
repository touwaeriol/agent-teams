<script setup lang="ts">
// The project header: ☰ (phones), project switcher, coordinator state, 等你 N, then the icons
// 资料库 / 讨论串 panel / 办公室 and the ⋯ menu (项目首页, 定时任务, 设置).
import { Clock, Home, Library, MoreHorizontal, PanelRight, Settings, Users } from "lucide-vue-next";
import { computed } from "vue";
import { RouterLink, useRouter } from "vue-router";
import type { CoordinatorView, ProjectView, ThreadView } from "@/api/views";
import Dot from "@/components/app/Dot.vue";
import IconButton from "@/components/common/IconButton.vue";
import IconMenu from "@/components/common/IconMenu.vue";
import NavButton from "@/components/shell/NavButton.vue";
import ProjectSwitcher from "@/components/shell/ProjectSwitcher.vue";
import { coordinatorState, waitingSummary, type SidePanel } from "./logic";

const props = defineProps<{ project: ProjectView; coordinator?: CoordinatorView; threads: ThreadView[]; panel: SidePanel }>();
const emit = defineEmits<{ panel: [panel: SidePanel]; schedules: [] }>();
const router = useRouter();
const state = computed(() => coordinatorState(props.coordinator));
const waiting = computed(() => waitingSummary(props.threads));
const repos = computed(() => props.project.repos?.map((r) => r.repo).join(" · "));

const COMMANDS: Record<string, () => void> = {
  home: () => void router.push(`/projects/${props.project.id}/overview`),
  schedules: () => emit("schedules"),
  settings: () => void router.push(`/projects/${props.project.id}/settings`),
};
</script>

<template>
  <header class="flex h-14 shrink-0 items-center justify-between gap-2 border-b bg-background px-2 sm:px-4 max-md:bg-panel">
    <div class="flex min-w-0 items-center gap-1">
      <NavButton />
      <ProjectSwitcher :project="project" />
      <span :class="['ml-1 hidden items-center gap-1.5 whitespace-nowrap text-xs text-muted-foreground', panel === 'library' ? '' : 'xl:flex']" :title="repos">
        <Dot :color="state.tone" />
        {{ state.text }}
      </span>
    </div>
    <span class="flex shrink-0 items-center gap-0.5">
      <RouterLink
        v-if="waiting.count > 0 && waiting.first"
        :to="`/projects/${project.id}/threads/${waiting.first.no}`"
        class="mr-1.5 flex h-8 items-center rounded-full bg-[#FFF4EC] px-2.5 text-xs text-[#B4530B] lg:hidden"
      >
        等你 {{ waiting.count }}
      </RouterLink>
      <IconButton label="资料库" :active="panel === 'library'" @click="emit('panel', panel === 'library' ? 'threads' : 'library')">
        <Library :stroke-width="1.8" />
      </IconButton>
      <IconButton
        :label="panel === 'threads' ? '收起讨论串' : '显示讨论串'"
        :active="panel === 'threads'"
        class="max-lg:hidden"
        @click="emit('panel', panel === 'threads' ? 'none' : 'threads')"
      >
        <PanelRight :stroke-width="1.8" />
      </IconButton>
      <IconButton label="办公室" :to="`/projects/${project.id}/office`" class="max-sm:hidden"><Users :stroke-width="1.8" /></IconButton>
      <IconMenu label="更多" @command="(c: string) => COMMANDS[c]?.()">
        <MoreHorizontal :stroke-width="1.8" />
        <template #menu>
          <el-dropdown-item command="home"><Home /> 项目首页</el-dropdown-item>
          <el-dropdown-item command="schedules"><Clock /> 定时任务</el-dropdown-item>
          <el-dropdown-item command="settings" divided><Settings /> 设置</el-dropdown-item>
        </template>
      </IconMenu>
    </span>
  </header>
</template>
