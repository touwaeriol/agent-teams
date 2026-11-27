<script setup lang="ts">
// 管理后台 · 后台任务: every background job the console runs (one registry on the server), its
// state and last run; run now, pause / resume, change the interval; a job's recorded runs below it.
import { computed, ref } from "vue";
import { jobs as jobActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminJobs, useJobRuns } from "@/api/queries";
import type { JobView } from "@/api/views";
import Card from "@/components/app/Card.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import Loading from "@/components/common/Loading.vue";
import AdminPage from "./AdminPage.vue";
import { auditTime, durationText, intervalText, jobState, TRIGGER_LABEL } from "./admin";

const list = useAdminJobs();
const jobs = computed(() => list.data.value ?? []);
const open = ref<string | null>(null);
const runs = useJobRuns(open);

const run = useAction((name: string) => jobActions.run(name), { invalidate: [keys.jobs], success: "已安排运行，几秒内开始" });
const pause = useAction((j: JobView) => jobActions.update(j.name, { paused: !j.paused }), {
  invalidate: [keys.jobs],
  success: (j) => (j.paused ? "已暂停" : "已恢复"),
});

const editing = ref<JobView | null>(null);
const seconds = ref(60);
const setEvery = useAction((v: { name: string; seconds: number }) => jobActions.update(v.name, { every_seconds: v.seconds }), {
  invalidate: [keys.jobs],
  success: "间隔已修改",
  onSuccess: () => (editing.value = null),
});
function editInterval(j: JobView) {
  editing.value = j;
  seconds.value = j.interval_seconds;
}

const dialogOpen = computed({ get: () => editing.value !== null, set: (v: boolean) => !v && (editing.value = null) });
const toggle = (name: string) => (open.value = open.value === name ? null : name);
</script>

<template>
  <AdminPage title="后台任务" description="控制面板定时运行的任务。暂停后只在手动运行时执行；修改间隔对所有副本生效。">
    <Loading v-if="list.isLoading.value" />
    <Card v-for="j in jobs" :key="j.name" class="gap-2" :aria-label="j.title" data-job="">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span class="text-[15px] font-semibold">{{ j.title }}</span>
        <el-tag :type="jobState(j).type" size="small" disable-transitions>{{ jobState(j).label }}</el-tag>
        <span class="text-xs text-muted-foreground">
          {{ intervalText(j.interval_seconds) }}<template v-if="j.override">（默认{{ intervalText(j.default_seconds) }}）</template>
          · {{ j.scope === "every" ? "每个副本都运行" : "一个副本运行" }}
        </span>
        <span class="flex-1" />
        <el-button size="small" :disabled="run.isPending.value || j.running || j.run_requested" @click="run.mutate(j.name)">立即运行</el-button>
        <el-button size="small" :disabled="pause.isPending.value" @click="pause.mutate(j)">{{ j.paused ? "恢复" : "暂停" }}</el-button>
        <el-button size="small" @click="editInterval(j)">修改间隔</el-button>
      </div>
      <p class="text-[13px] leading-5 text-muted-foreground">{{ j.description }}</p>
      <dl class="grid grid-cols-[84px_1fr] gap-y-1 text-[13px]">
        <dt class="text-muted-foreground">上次运行</dt>
        <dd>
          <template v-if="j.last_started_at && j.last_finished_at">
            {{ auditTime(j.last_started_at) }}，用时 {{ durationText(j.last_started_at, j.last_finished_at) }}
            <span v-if="j.last_replica" class="font-mono text-xs text-muted-foreground">{{ j.last_replica }}</span>
          </template>
          <template v-else>还没运行</template>
        </dd>
        <template v-if="j.last_error">
          <dt class="text-muted-foreground">错误</dt>
          <dd class="whitespace-pre-wrap break-all text-[#B42318] dark:text-red-400">{{ j.last_error }}</dd>
        </template>
        <template v-else-if="j.last_summary">
          <dt class="text-muted-foreground">结果</dt>
          <dd>{{ j.last_summary }}</dd>
        </template>
        <template v-if="j.next_run_at">
          <dt class="text-muted-foreground">下次</dt>
          <dd>约 {{ auditTime(j.next_run_at) }}</dd>
        </template>
        <dt class="text-muted-foreground">累计</dt>
        <dd>运行 {{ j.runs }} 次<template v-if="j.failures">，失败 {{ j.failures }} 次</template></dd>
      </dl>
      <button type="button" class="self-start text-xs text-muted-foreground underline-offset-2 hover:underline" @click="toggle(j.name)">
        {{ open === j.name ? "收起运行记录" : "查看运行记录" }}
      </button>
      <div v-if="open === j.name" class="flex flex-col gap-1 border-t border-[#F1EFEA] pt-2 text-[13px] dark:border-border" data-runs="">
        <p v-if="!runs.data.value?.length" class="text-muted-foreground">没有记录（只记录做了事、失败或手动的运行，保留 30 天）。</p>
        <div v-for="r in runs.data.value ?? []" :key="r.id" class="flex flex-wrap items-baseline gap-x-3">
          <span class="font-mono text-xs text-muted-foreground">{{ auditTime(r.started_at) }}</span>
          <span class="text-xs text-muted-foreground">{{ TRIGGER_LABEL[r.trigger] ?? r.trigger }} · {{ durationText(r.started_at, r.finished_at) }}</span>
          <span v-if="!r.ok" class="whitespace-pre-wrap break-all text-[#B42318] dark:text-red-400">{{ r.error }}</span>
          <span v-else>{{ r.summary || "完成" }}</span>
        </div>
      </div>
    </Card>

    <FormDialog
      v-model:open="dialogOpen"
      :title="`修改间隔：${editing?.title ?? ''}`"
      submit-label="保存"
      :busy="setEvery.isPending.value"
      @submit="editing && setEvery.mutate({ name: editing.name, seconds })"
    >
      <div class="flex items-center gap-2 text-[13px]">
        每
        <el-input-number v-model="seconds" :min="1" :max="604800" controls-position="right" />
        秒运行一次
      </div>
      <p class="text-xs text-muted-foreground">默认{{ intervalText(editing?.default_seconds ?? 0) }}。</p>
      <el-button v-if="editing?.override" class="self-start" size="small" @click="editing && setEvery.mutate({ name: editing.name, seconds: 0 })">恢复默认</el-button>
    </FormDialog>
  </AdminPage>
</template>
