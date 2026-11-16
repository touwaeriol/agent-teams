<script setup lang="ts">
import { RouterLink } from "vue-router";
import type { ProjectView } from "@/api/views";
import ProjectIcon from "@/components/app/ProjectIcon.vue";
import { cn } from "@/lib/utils";
import ProjectMenu from "./ProjectMenu.vue";

defineProps<{ project: ProjectView; current: boolean }>();
const emit = defineEmits<{ navigate: [] }>();
</script>

<template>
  <div :class="cn('group relative flex h-9 items-center rounded-lg', current ? 'bg-accent font-semibold' : 'hover:bg-accent/60')">
    <RouterLink
      :to="`/projects/${project.id}`"
      :aria-current="current ? 'page' : undefined"
      class="flex h-full min-w-0 flex-1 items-center gap-2.5 px-2.5"
      @click="emit('navigate')"
    >
      <ProjectIcon :project="project" :size="22" />
      <span class="min-w-0 flex-1 truncate">{{ project.name }}</span>
      <span v-if="project.waiting > 0" class="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-[#C2570C] px-[5px] text-[11px] font-semibold text-white group-hover:hidden">
        {{ project.waiting }}
      </span>
    </RouterLink>
    <ProjectMenu :project="project" />
  </div>
</template>
