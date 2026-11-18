<script setup lang="ts">
/**
 * 项目编制 (shared-members.md §9): one project's members — its headcount — with the thread and task
 * each works on now, by role. "加一人" raises the role's 编制 in the draft (shown as 2→3 until
 * saved); saving the templates staffs every project to it.
 */
import { computed, ref, watch } from "vue";
import { useOfficeSnapshot, useProjects } from "@/api/queries";
import type { RoleView } from "@/api/views";
import Card from "@/components/app/Card.vue";
import { countText, headcountText, staffGroups } from "./staff";

const props = defineProps<{ roles: RoleView[]; saved: RoleView[] }>();
const emit = defineEmits<{ add: [role: string] }>();
const projects = useProjects();
const pid = ref("");
watch(() => projects.data.value, (list) => !pid.value && list?.length && (pid.value = list[0].id), { immediate: true });
const snapshot = useOfficeSnapshot(pid);
const groups = computed(() => (snapshot.data.value ? staffGroups(snapshot.data.value, props.roles, props.saved) : []));
</script>

<template>
  <Card id="project-staff" class="scroll-mt-4 gap-3">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="flex flex-col gap-1">
        <span class="font-semibold">项目编制</span>
        <span class="text-xs leading-5 text-muted-foreground">成员是项目的固定员工，所有讨论串共用；统筹和组长是管理岗，不算编制。编制按岗位定、所有项目一样：“加一人”把这个岗位的编制加一，保存后每个项目都按新编制配齐。</span>
      </div>
      <el-select v-if="(projects.data.value ?? []).length > 1" v-model="pid" class="at-select-36 at-text-13 !w-56" aria-label="项目">
        <el-option v-for="p in projects.data.value ?? []" :key="p.id" :value="p.id" :label="p.name" />
      </el-select>
    </div>
    <span v-if="!(projects.data.value ?? []).length" class="text-[13px] text-muted-foreground">还没有项目。</span>
    <template v-else>
      <span v-if="groups.length" class="text-xs text-muted-foreground" data-testid="staff-headcount">{{ headcountText(groups) }}</span>
      <section v-for="g in groups" :key="g.role" class="flex flex-col gap-1.5 rounded-lg bg-background px-3 py-2.5" :aria-label="g.title">
        <span class="flex items-center justify-between gap-2 text-[13px]">
          <span>
            <span class="font-semibold">{{ g.title }}</span>
            <span class="ml-2 text-xs" :class="g.count === g.saved ? 'text-muted-foreground' : 'text-warn'" data-testid="staff-count">{{ countText(g) }}</span>
          </span>
          <el-button v-if="g.template" size="small" text type="primary" title="所有项目的这个岗位都加一人（保存后生效）" @click="emit('add', g.role)">加一人（所有项目）</el-button>
        </span>
        <span v-if="!g.rows.length" class="text-xs text-muted-foreground">这个项目还没有这个岗位的人（第一次开讨论串时配齐）。</span>
        <div v-for="r in g.rows" :key="r.id" class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-[13px] sm:grid-cols-[5.5rem_minmax(0,14rem)_minmax(0,1fr)]">
          <span class="font-mono">{{ r.name }}</span>
          <span class="truncate text-muted-foreground" :title="r.thread">{{ r.thread || "—" }}</span>
          <span class="col-start-2 truncate sm:col-start-auto" :class="r.busy ? '' : 'text-muted-foreground'" :title="r.work">{{ r.work }}</span>
        </div>
      </section>
    </template>
  </Card>
</template>
