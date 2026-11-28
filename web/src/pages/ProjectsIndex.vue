<script setup lang="ts">
// /projects: every project as a card (pinned, then the others; archived ones dimmed), a search box
// and 新建项目. Sessions are not listed here: they are made from 新建 and live in the sidebar.
import { $t } from "@/i18n";
import { FolderPlus, Search } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { useProjects } from "@/api/queries";
import ProjectIcon from "@/components/app/ProjectIcon.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { cn, timeAgo } from "@/lib/utils";
import { projectGroups } from "./projects";

const projects = useProjects();
const q = ref("");
const searching = ref(false);
const groups = computed(() => projectGroups(projects.data.value ?? [], q.value));
const none = computed(() => (projects.data.value ?? []).every((p) => p.kind === "session"));
</script>

<template>
  <Loading v-if="projects.isLoading.value" class="h-full" />
  <main v-else class="px-4 pb-14 pt-4 sm:px-8 sm:pt-10">
    <div class="mx-auto flex max-w-[960px] flex-col gap-6">
      <div class="flex items-center gap-2">
        <NavButton />
        <h1 class="flex-1 text-2xl font-semibold">{{ $t("项目") }}</h1>
        <label v-if="searching" class="flex h-8 w-56 items-center gap-2 rounded-lg border px-2 text-muted-foreground">
          <Search class="size-3.5" />
          <input v-model="q" autofocus :placeholder="$t('搜索项目')" class="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none" @keydown.esc="(searching = false), (q = '')" />
        </label>
        <button v-else type="button" :aria-label="$t('搜索项目')" class="rounded-lg p-2 text-muted-foreground hover:bg-accent hover:text-foreground" @click="searching = true">
          <Search class="size-4" />
        </button>
        <RouterLink to="/projects/new" class="inline-flex h-8 items-center rounded-lg bg-primary px-3 text-[13px] font-semibold text-primary-foreground hover:bg-primary/90">{{ $t("新建项目") }}</RouterLink>
      </div>
      <EmptyState v-if="none" :title="$t('还没有项目')" :description="$t('项目适合长期协作：关联仓库，和项目统筹说需求，它会开讨论串让团队去做。临时的活直接点左上角「新建」开个会话就行。')">
        <template #icon><FolderPlus /></template>
      </EmptyState>
      <p v-else-if="groups.length === 0" class="text-[13px] text-muted-foreground">{{ $t("没有匹配的项目") }}</p>
      <section v-for="g in groups" :key="g.title" class="flex flex-col gap-3">
        <h2 class="text-[13px] text-muted-foreground">{{ g.title }}</h2>
        <div class="grid gap-3 sm:grid-cols-2">
          <RouterLink
            v-for="p in g.items"
            :key="p.id"
            :to="`/projects/${p.id}`"
            :class="cn('flex items-center gap-3 rounded-xl bg-raised/60 px-4 py-3 hover:bg-raised', p.archived && 'opacity-60')"
            data-testid="project-card"
          >
            <ProjectIcon :project="p" :size="32" />
            <span class="flex min-w-0 flex-col">
              <span class="truncate text-[14px] font-medium">{{ p.name }}</span>
              <span class="truncate text-xs text-muted-foreground">{{ timeAgo(p.updated_at) }}<template v-if="p.archived"> {{ $t("· 已归档") }}</template></span>
            </span>
          </RouterLink>
        </div>
      </section>
    </div>
  </main>
</template>
