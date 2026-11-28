<script setup lang="ts">
// 管理后台 · 后台任务: every scheduled task the console runs (all on River; built-in ones cannot be
// deleted), its state and last run; run now, pause / resume, change the period (an interval or a
// cron schedule); a job's recorded runs below it.
import { $t } from "@/i18n";
import { computed, ref } from "vue";
import { jobs as jobActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminJobs, useJobRuns } from "@/api/queries";
import type { JobView } from "@/api/views";
import Card from "@/components/app/Card.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import Loading from "@/components/common/Loading.vue";
import AdminPage from "./AdminPage.vue";
import { auditTime, browserZone, durationText, intervalText, jobState, scheduleText, TRIGGER_LABEL } from "./admin";

const list = useAdminJobs();
const jobs = computed(() => list.data.value ?? []);
const open = ref<string | null>(null);
const runs = useJobRuns(open);

const run = useAction((name: string) => jobActions.run(name), { invalidate: [keys.jobs], success: $t("已安排运行，几秒内开始") });
const pause = useAction((j: JobView) => jobActions.update(j.name, { paused: !j.paused }), {
  invalidate: [keys.jobs],
  success: (j) => (j.paused ? $t("已暂停") : $t("已恢复")),
});

type Period = { name: string; every_seconds: number } | { name: string; cron: string; timezone: string };

const editing = ref<JobView | null>(null);
const mode = ref<"interval" | "cron">("interval");
const seconds = ref(60);
const cron = ref("");
const zone = ref("");
const setPeriod = useAction(({ name, ...body }: Period) => jobActions.update(name, body), {
  invalidate: [keys.jobs],
  success: $t("周期已修改"),
  onSuccess: () => (editing.value = null),
});
function editPeriod(j: JobView) {
  editing.value = j;
  mode.value = j.cron ? "cron" : "interval";
  seconds.value = j.interval_seconds;
  cron.value = j.cron;
  zone.value = j.timezone || browserZone();
}
function save() {
  const j = editing.value;
  if (!j) return;
  if (mode.value === "cron") setPeriod.mutate({ name: j.name, cron: cron.value.trim(), timezone: zone.value.trim() });
  else setPeriod.mutate({ name: j.name, every_seconds: seconds.value });
}

const dialogOpen = computed({ get: () => editing.value !== null, set: (v: boolean) => !v && (editing.value = null) });
const toggle = (name: string) => (open.value = open.value === name ? null : name);
</script>

<template>
  <AdminPage :title="$t('后台任务')" :description="$t('控制面板的定时任务，都在 River 上运行，由任意一个副本执行、同一时刻只跑一份。内置任务不能删除，可以暂停或修改周期（间隔或 cron）；暂停后只在手动运行时执行。')">
    <Loading v-if="list.isLoading.value" />
    <Card v-for="j in jobs" :key="j.name" class="gap-2" :aria-label="j.title" data-job="">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span class="text-[15px] font-semibold">{{ j.title }}</span>
        <el-tag :type="jobState(j).type" size="small" disable-transitions>{{ jobState(j).label }}</el-tag>
        <el-tag v-if="j.kind === 'builtin'" type="info" size="small" effect="plain" disable-transitions>{{ $t("内置") }}</el-tag>
        <span class="text-xs text-muted-foreground">
          {{ scheduleText(j) }}<template v-if="j.override">{{ $t("（默认{default_seconds}）", { default_seconds: intervalText(j.default_seconds) }) }}</template>
        </span>
        <span class="flex-1" />
        <el-button size="small" :disabled="run.isPending.value || j.running || j.run_requested" @click="run.mutate(j.name)">{{ $t("立即运行") }}</el-button>
        <el-button size="small" :disabled="pause.isPending.value" @click="pause.mutate(j)">{{ j.paused ? $t("恢复") : $t("暂停") }}</el-button>
        <el-button size="small" @click="editPeriod(j)">{{ $t("修改周期") }}</el-button>
      </div>
      <p class="text-[13px] leading-5 text-muted-foreground">{{ j.description }}</p>
      <dl class="grid grid-cols-[84px_1fr] gap-y-1 text-[13px]">
        <dt class="text-muted-foreground">{{ $t("上次运行") }}</dt>
        <dd>
          <template v-if="j.last_started_at && j.last_finished_at">
            {{ $t("{last_started_at}，用时 {durationText}", { last_started_at: auditTime(j.last_started_at), durationText: durationText(j.last_started_at, j.last_finished_at) }) }}
            <span v-if="j.last_replica" class="font-mono text-xs text-muted-foreground">{{ j.last_replica }}</span>
          </template>
          <template v-else>{{ $t("还没运行") }}</template>
        </dd>
        <template v-if="j.last_error">
          <dt class="text-muted-foreground">{{ $t("错误") }}</dt>
          <dd class="whitespace-pre-wrap break-all text-[#B42318] dark:text-red-400">{{ j.last_error }}</dd>
        </template>
        <template v-else-if="j.last_summary">
          <dt class="text-muted-foreground">{{ $t("结果") }}</dt>
          <dd>{{ j.last_summary }}</dd>
        </template>
        <template v-if="j.next_run_at">
          <dt class="text-muted-foreground">{{ $t("下次") }}</dt>
          <dd>{{ $t("约 {next_run_at}", { next_run_at: auditTime(j.next_run_at) }) }}</dd>
        </template>
        <dt class="text-muted-foreground">{{ $t("累计") }}</dt>
        <dd>{{ $t("运行 {runs} 次", { runs: j.runs }) }}<template v-if="j.failures">{{ $t("，失败 {failures} 次", { failures: j.failures }) }}</template></dd>
      </dl>
      <button type="button" class="self-start text-xs text-muted-foreground underline-offset-2 hover:underline" @click="toggle(j.name)">
        {{ open === j.name ? $t("收起运行记录") : $t("查看运行记录") }}
      </button>
      <div v-if="open === j.name" class="flex flex-col gap-1 border-t border-[#F1EFEA] pt-2 text-[13px] dark:border-border" data-runs="">
        <p v-if="!runs.data.value?.length" class="text-muted-foreground">{{ $t("没有记录（只记录做了事、失败或手动的运行，保留 30 天）。") }}</p>
        <div v-for="r in runs.data.value ?? []" :key="r.id" class="flex flex-wrap items-baseline gap-x-3">
          <span class="font-mono text-xs text-muted-foreground">{{ auditTime(r.started_at) }}</span>
          <span class="text-xs text-muted-foreground">{{ TRIGGER_LABEL[r.trigger] ?? r.trigger }} · {{ durationText(r.started_at, r.finished_at) }}</span>
          <span v-if="!r.ok" class="whitespace-pre-wrap break-all text-[#B42318] dark:text-red-400">{{ r.error }}</span>
          <span v-else>{{ r.summary || $t("完成") }}</span>
        </div>
      </div>
    </Card>

    <FormDialog
      v-model:open="dialogOpen"
      :title="$t('修改周期：{n}', { n: editing?.title ?? '' })"
      :submit-label="$t('保存')"
      :busy="setPeriod.isPending.value"
      @submit="save"
    >
      <el-radio-group v-model="mode" size="small" class="self-start">
        <el-radio-button value="interval">{{ $t("固定间隔") }}</el-radio-button>
        <el-radio-button value="cron">cron</el-radio-button>
      </el-radio-group>
      <div v-if="mode === 'interval'" class="flex items-center gap-2 text-[13px]">
        {{ $t("每") }}
        <el-input-number v-model="seconds" :min="1" :max="604800" controls-position="right" />
        {{ $t("秒运行一次") }}
      </div>
      <template v-else>
        <el-input v-model="cron" :placeholder="$t('例如 0 3 * * *（每天 3 点）或 {hourly}', { hourly: '@hourly' })" :aria-label="$t('cron 表达式')" class="font-mono" />
        <el-input v-model="zone" :placeholder="$t('时区，例如 Asia/Shanghai')" :aria-label="$t('时区')" />
        <p class="text-xs text-muted-foreground">{{ $t("五段：分 时 日 月 周，也可写 {daily}、{hourly}；两次运行至少间隔 10 秒。", { daily: "@daily", hourly: "@hourly" }) }}</p>
      </template>
      <p class="text-xs text-muted-foreground">{{ $t("默认{intervalText}。", { intervalText: intervalText(editing?.default_seconds ?? 0) }) }}</p>
      <el-button
        v-if="editing?.override"
        class="self-start"
        size="small"
        @click="editing && setPeriod.mutate({ name: editing.name, every_seconds: 0 })"
      >{{ $t("恢复默认") }}</el-button>
    </FormDialog>
  </AdminPage>
</template>
