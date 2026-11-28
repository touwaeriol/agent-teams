<script setup lang="ts">
import { $t } from "@/i18n";
import { RouterLink } from "vue-router";
import type { ProjectView } from "@/api/views";
import { CodeXml } from "lucide-vue-next";
import ProjectIcon from "@/components/app/ProjectIcon.vue";
import SessionMark from "@/components/app/session/SessionMark.vue";
import { homeOf, isSession, sessionMark } from "@/components/app/session/session";
import { cn } from "@/lib/utils";
import ProjectMenu from "./ProjectMenu.vue";

defineProps<{ project: ProjectView; current: boolean }>();
const emit = defineEmits<{ navigate: [] }>();
</script>

<template>
  <div :class="cn('group relative flex h-9 items-center rounded-lg', current ? 'bg-accent font-semibold' : 'hover:bg-accent/60')">
    <RouterLink
      :to="homeOf(project)"
      :aria-current="current ? 'page' : undefined"
      class="flex h-full min-w-0 flex-1 items-center gap-2.5 px-2.5"
      @click="emit('navigate')"
    >
      <span v-if="isSession(project)" :aria-label="$t('会话')" :title="$t('会话')" class="flex size-[22px] shrink-0 items-center justify-center rounded-md bg-raised text-muted-foreground">
        <CodeXml class="size-3.5" :stroke-width="2" />
      </span>
      <ProjectIcon v-else :project="project" :size="22" />
      <span class="min-w-0 flex-1 truncate">{{ project.name }}</span>
      <SessionMark v-if="sessionMark(project)" :mark="sessionMark(project)!" class="group-hover:hidden" />
      <span v-if="project.waiting > 0" class="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-[#C2570C] px-[5px] text-[11px] font-semibold text-white group-hover:hidden">
        {{ project.waiting }}
      </span>
    </RouterLink>
    <ProjectMenu :project="project" />
  </div>
</template>
