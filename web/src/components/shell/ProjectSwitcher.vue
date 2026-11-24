<script setup lang="ts">
// The project name in the header opens this: search, every project, 新建项目.
import { Check, ChevronDown, Plus, Search } from "lucide-vue-next";
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { useProjects } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import ProjectIcon from "@/components/app/ProjectIcon.vue";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import { homeOf } from "@/components/app/session/session";
import { matchesProject } from "./projects";

const props = defineProps<{ project: ProjectView }>();
const router = useRouter();
const projects = useProjects();
const q = ref("");
const list = computed(() => (projects.data.value ?? []).filter((p) => !p.archived || p.id === props.project.id).filter((p) => matchesProject(p, q.value)));
const go = (command: string) => {
  const p = (projects.data.value ?? []).find((x) => x.id === command);
  void router.push(command === "new" ? "/projects/new" : p ? homeOf(p) : `/projects/${command}`);
};
const vFocus = { mounted: (el: HTMLElement) => setTimeout(() => el.focus(), 0) };
</script>

<template>
  <DropdownMenu bare @command="go" @open="(open: boolean) => !open && (q = '')">
    <button type="button" aria-label="切换项目" class="flex h-9 min-w-0 items-center gap-2 rounded-lg px-1.5 hover:bg-raised aria-expanded:bg-raised">
      <ProjectIcon :project="project" :size="24" />
      <span class="truncate text-[15px] font-semibold">{{ project.name }}</span>
      <ChevronDown class="size-4 shrink-0 text-muted-foreground" :stroke-width="1.8" />
    </button>
    <template #menu>
      <div class="w-72 p-1.5">
        <label class="mb-1 flex h-8 items-center gap-2 rounded-md border border-input px-2 text-muted-foreground" @keydown.stop>
          <Search class="size-3.5" :stroke-width="1.8" />
          <input v-model="q" v-focus placeholder="搜索项目" class="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" />
        </label>
        <el-dropdown-menu class="scroll-thin max-h-72 overflow-y-auto !p-0">
          <el-dropdown-item v-for="p in list" :key="p.id" :command="p.id" class="!gap-2.5">
            <ProjectIcon :project="p" :size="20" />
            <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
            <Check v-if="p.id === project.id" class="text-muted-foreground" />
          </el-dropdown-item>
          <p v-if="list.length === 0" class="px-2 py-3 text-xs text-muted-foreground">没有匹配的项目</p>
        </el-dropdown-menu>
        <el-dropdown-menu class="!p-0">
          <el-dropdown-item command="new" divided><Plus /> 新建项目</el-dropdown-item>
        </el-dropdown-menu>
      </div>
    </template>
  </DropdownMenu>
</template>
