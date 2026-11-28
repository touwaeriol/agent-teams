<script setup lang="ts">
// Create or edit one schedule: name, whom it wakes (the coordinator or a thread's lead), recurring
// (a preset or a cron expression) or once (a date and time), and the message.
import { $t } from "@/i18n";
import { computed, ref } from "vue";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useThreads } from "@/api/queries";
import Field from "@/components/common/Field.vue";
import { CRON_PRESETS, CUSTOM, looksLikeCron, presetOf } from "@/lib/schedules";
import { draftValid, scheduleBody, type Draft } from "./schedule";

const props = defineProps<{ pid: string }>();
const draft = defineModel<Draft>({ required: true });
const emit = defineEmits<{ done: [] }>();
const preset = ref(presetOf(draft.value.cron));
const threads = useThreads(() => props.pid);
const targets = computed(() => (threads.data.value ?? []).filter((t) => t.status !== "archived" || t.no === draft.value.threadNo));
const COORDINATOR = 0;
const save = useAction(
  () => {
    const body = scheduleBody(draft.value);
    if (draft.value.id) return scheduleActions.update(draft.value.id, body);
    return scheduleActions.create(props.pid, { ...body, enabled: true, ...(draft.value.threadNo ? { thread_no: draft.value.threadNo } : {}) });
  },
  { invalidate: () => [keys.schedules(props.pid)], success: $t("已保存"), onSuccess: () => emit("done") },
);
const valid = computed(() => draftValid(draft.value));
const set = (patch: Partial<Draft>) => (draft.value = { ...draft.value, ...patch });
const recipient = computed(() => (draft.value.threadNo ? $t("#{threadNo} 的组长", { threadNo: draft.value.threadNo }) : $t("统筹")));
const disabledDate = (d: Date) => d.getTime() < new Date().setHours(0, 0, 0, 0);

function choose(id: string) {
  preset.value = id;
  const p = CRON_PRESETS.find((x) => x.id === id);
  if (p) set({ cron: p.cron });
}
function typeCron(cron: string) {
  set({ cron });
  preset.value = presetOf(cron);
}
const submit = () => valid.value && save.mutate();
</script>

<template>
  <form class="flex flex-col gap-4" @submit.prevent="submit">
    <Field v-slot="{ id }" :label="$t('名称')">
      <el-input :id="id" :model-value="draft.name" :placeholder="$t('例如：每日进展')" autofocus :maxlength="100" @update:model-value="(v: string) => set({ name: v })" />
    </Field>
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" :label="$t('发给谁')" :hint="draft.id ? $t('创建后不能改') : undefined">
        <el-select :id="id" :model-value="draft.threadNo ?? COORDINATOR" :disabled="Boolean(draft.id)" filterable @update:model-value="(v: number) => set({ threadNo: v || null })">
          <el-option :value="COORDINATOR" :label="$t('项目统筹')" />
          <el-option v-for="t in targets" :key="t.no" :value="t.no" :label="$t('#{no} {title} 的组长', { no: t.no, title: t.title })" />
        </el-select>
      </Field>
      <Field :label="$t('类型')">
        <el-radio-group :model-value="draft.kind" @update:model-value="(v: string | number | boolean | undefined) => set({ kind: v === 'once' ? 'once' : 'cron' })">
          <el-radio-button value="cron">{{ $t("周期") }}</el-radio-button>
          <el-radio-button value="once">{{ $t("一次") }}</el-radio-button>
        </el-radio-group>
      </Field>
    </div>
    <div v-if="draft.kind === 'cron'" class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" :label="$t('什么时候')">
        <el-select :id="id" :model-value="preset" @update:model-value="choose">
          <el-option v-for="p in CRON_PRESETS" :key="p.id" :value="p.id" :label="p.label" />
          <el-option :value="CUSTOM" :label="$t('自定义（cron）')" />
        </el-select>
      </Field>
      <Field v-slot="{ id }" :label="$t('cron 表达式')" :hint="$t('分 时 日 月 周，或 {daily}', { daily: '@daily' })">
        <el-input :id="id" :model-value="draft.cron" class="at-mono" :aria-invalid="!looksLikeCron(draft.cron)" @update:model-value="typeCron" />
      </Field>
    </div>
    <Field v-else v-slot="{ id }" :label="$t('触发时间')">
      <el-date-picker
        :id="id"
        :model-value="draft.runAt"
        type="datetime"
        format="YYYY-MM-DD HH:mm"
        :disabled-date="disabledDate"
        :clearable="false"
        :placeholder="$t('选择日期和时间')"
        class="!w-full"
        @update:model-value="(v: Date) => set({ runAt: v })"
      />
    </Field>
    <Field v-slot="{ id }" :label="$t('发给{recipient}的消息', { recipient })">
      <el-input
        :id="id"
        :model-value="draft.message"
        type="textarea"
        :rows="4"
        :placeholder="$t('例如：汇总昨天各讨论串的进展，有卡住的告诉我')"
        :maxlength="20000"
        @update:model-value="(v: string) => set({ message: v })"
      />
    </Field>
    <span class="flex justify-end gap-2">
      <el-button @click="emit('done')">{{ $t("取消") }}</el-button>
      <el-button type="primary" native-type="submit" class="!ml-0" :disabled="!valid || save.isPending.value">{{ $t("保存") }}</el-button>
    </span>
  </form>
</template>
