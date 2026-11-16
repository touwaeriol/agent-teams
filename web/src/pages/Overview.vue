<script setup lang="ts">
// Project home: the project's threads as cards with filters, and on the side what waits for
// you and project memory.
import { computed, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { errorText } from "@/api/mutations";
import { useOverview, useRoles } from "@/api/queries";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { teamSummary, type Filter } from "./overview/logic";
import MemoryBox from "./overview/MemoryBox.vue";
import ThreadCards from "./overview/ThreadCards.vue";
import WaitingBox from "./overview/WaitingBox.vue";

const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const overview = useOverview(pid);
const roles = useRoles();
const filter = ref<Filter>("all");
const o = computed(() => overview.data.value);
const repos = computed(() => (o.value?.project.repos?.length ? o.value.project.repos.map((r) => r.repo).join("、") : "未关联仓库"));
const links = computed(() => [
  { to: `/projects/${pid.value}/settings`, label: "项目设置", primary: false },
  { to: `/projects/${pid.value}/office`, label: "办公室", primary: false },
  { to: `/projects/${pid.value}`, label: "和项目统筹说话", primary: true },
]);
</script>

<template>
  <EmptyState v-if="overview.error.value" title="打不开项目首页" :description="errorText(overview.error.value)" />
  <Loading v-else-if="!o" class="h-full" />
  <main v-else class="px-4 pb-12 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[1100px] flex-col gap-6">
      <header class="flex flex-wrap items-end justify-between gap-4">
        <div class="flex items-start gap-1">
          <NavButton />
          <div class="flex flex-col gap-1.5">
            <h1 class="text-2xl font-semibold tracking-tight">{{ o.project.name }}</h1>
            <span class="flex flex-wrap gap-2 text-[13px] text-muted-foreground">
              <span class="font-mono text-foreground">{{ repos }}</span>
              <span>工作方式 {{ o.project.work_mode === "worktree" ? "worktree" : "独立克隆" }}</span>
              <span v-if="roles.data.value?.roles">团队模板：{{ teamSummary(roles.data.value.roles) }}</span>
            </span>
          </div>
        </div>
        <div class="flex gap-2">
          <RouterLink v-for="l in links" :key="l.label" v-slot="{ href, navigate }" :to="l.to" custom>
            <el-button tag="a" :href="href" :type="l.primary ? 'primary' : undefined" class="!ml-0 !h-[38px]" @click="navigate">{{ l.label }}</el-button>
          </RouterLink>
        </div>
      </header>
      <div class="flex flex-wrap items-start gap-5">
        <ThreadCards v-model:filter="filter" :overview="o" />
        <aside class="flex min-w-0 flex-[1_1_300px] flex-col gap-3.5">
          <WaitingBox :pid="pid" :overview="o" />
          <MemoryBox :pid="pid" :overview="o" />
        </aside>
      </div>
    </div>
  </main>
</template>
