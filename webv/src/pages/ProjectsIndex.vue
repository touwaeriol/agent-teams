<script setup lang="ts">
// /projects: open the most recent project, or invite to create the first one.
import { FolderPlus } from "lucide-vue-next";
import { computed, watchEffect } from "vue";
import { RouterLink, useRouter } from "vue-router";
import { useProjects } from "@/api/queries";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { recentProject } from "./projects";

const router = useRouter();
const projects = useProjects();
const recent = computed(() => recentProject(projects.data.value ?? []));
watchEffect(() => {
  if (recent.value) void router.replace(`/projects/${recent.value.id}`);
});
</script>

<template>
  <Loading v-if="projects.isLoading.value || recent" class="h-full" />
  <div v-else class="p-4">
    <NavButton />
    <div class="mx-auto max-w-xl pt-16">
      <EmptyState title="还没有项目" description="项目是入口：关联一个 GitHub 仓库，和项目统筹说需求，它会开讨论串让团队去做。">
        <template #icon><FolderPlus /></template>
        <template #action>
          <RouterLink to="/projects/new" class="inline-flex h-9 items-center rounded-lg bg-primary px-3.5 text-sm font-semibold text-primary-foreground hover:bg-primary/90">新建项目</RouterLink>
        </template>
      </EmptyState>
    </div>
  </div>
</template>
