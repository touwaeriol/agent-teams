<script setup lang="ts">
// The project sidebar of the design (Sidebar board): projects and sessions in one list (told apart by
// their icon), plus 新建 (a session, like Claude Code) / 搜索 / 项目 (the projects page; + makes one) / 资料库 / 共享文件, archived ones folded, nodes and the user at the bottom.
import { ChevronRight, FolderKanban, FolderOpen, LayoutGrid, Plus, Search } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { useProjects } from "@/api/queries";
import type { UserView } from "@/api/views";
import Logo from "@/components/common/Logo.vue";
import { cn } from "@/lib/utils";
import ProjectRow from "./ProjectRow.vue";
import { sidebarGroups } from "./projects";
import SideLink from "./SideLink.vue";
import SidebarFooter from "./SidebarFooter.vue";

defineProps<{ me: UserView }>();
const emit = defineEmits<{ navigate: [] }>();
const route = useRoute();

const pid = computed(() => (typeof route.params.pid === "string" ? route.params.pid : ""));
const projects = useProjects();
const q = ref("");
const showArchived = ref(false);
const view = computed(() => sidebarGroups(projects.data.value ?? [], q.value));
const current = computed(() => (projects.data.value ?? []).find((p) => p.id === pid.value));
const nav = () => emit("navigate");
</script>

<template>
  <nav aria-label="项目" class="flex h-full w-[248px] shrink-0 flex-col border-r bg-sidebar">
    <RouterLink :to="current && current.kind !== 'session' ? `/projects/${current.id}/overview` : '/'" class="flex h-14 items-center gap-2.5 px-[18px] font-semibold" @click="nav">
      <Logo class="size-5" />
      Agent Teams
    </RouterLink>
    <div class="flex flex-col gap-0.5 px-3">
      <SideLink to="/sessions/new" strong @navigate="nav">
        <template #icon><Plus /></template>
        新建
      </SideLink>
      <label class="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-muted-foreground">
        <Search class="size-4" :stroke-width="1.8" />
        <input v-model="q" placeholder="搜索项目和会话" class="min-w-0 flex-1 bg-transparent text-foreground outline-none placeholder:text-muted-foreground" />
      </label>
      <div class="group relative">
        <SideLink to="/projects" @navigate="nav">
          <template #icon><FolderKanban /></template>
          项目
        </SideLink>
        <RouterLink
          to="/projects/new"
          aria-label="新建项目"
          title="新建项目"
          class="absolute right-1.5 top-1/2 hidden -translate-y-1/2 rounded-md p-1 text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:flex group-hover:flex"
          @click="nav"
        >
          <Plus class="size-4" />
        </RouterLink>
      </div>
      <SideLink v-if="current" :to="`/projects/${current.id}/deliverables`" @navigate="nav">
        <template #icon><LayoutGrid /></template>
        资料库
      </SideLink>
      <SideLink v-if="current" :to="`/projects/${current.id}/files`" @navigate="nav">
        <template #icon><FolderOpen /></template>
        共享文件
      </SideLink>
    </div>
    <div class="scroll-thin flex-1 overflow-y-auto px-3 py-4">
      <div v-for="g in view.groups" :key="g.title" class="mb-3.5">
        <span class="block px-2.5 pb-1.5 text-xs text-muted-foreground">{{ g.title }}</span>
        <div class="flex flex-col gap-px">
          <ProjectRow v-for="p in g.items" :key="p.id" :project="p" :current="p.id === pid" @navigate="nav" />
        </div>
      </div>
      <p v-if="projects.data.value && view.shown.length === 0" class="px-2.5 text-xs text-muted-foreground">{{ q ? "没有匹配的项目或会话" : "还没有项目或会话" }}</p>
      <button
        v-if="view.archived.length > 0"
        type="button"
        class="flex items-center gap-1.5 px-2.5 text-xs text-muted-foreground hover:text-foreground"
        @click="showArchived = !showArchived"
      >
        已归档 {{ view.archived.length }} <ChevronRight :class="cn('size-2.5 transition-transform', showArchived && 'rotate-90')" :stroke-width="2" />
      </button>
      <div v-if="showArchived" class="mt-1.5 flex flex-col gap-px opacity-80">
        <ProjectRow v-for="p in view.archived" :key="p.id" :project="p" :current="p.id === pid" @navigate="nav" />
      </div>
    </div>
    <SidebarFooter :me="me" @navigate="nav" />
  </nav>
</template>
