<script setup lang="ts">
// Create or edit one schedule: name, a preset or a cron expression, the message for the coordinator.
import { computed, ref } from "vue";
import { schedules as scheduleActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import Field from "@/components/common/Field.vue";
import { CRON_PRESETS, CUSTOM, looksLikeCron, presetOf } from "@/lib/schedules";
import { draftValid, scheduleBody, type Draft } from "./schedule";

const props = defineProps<{ pid: string }>();
const draft = defineModel<Draft>({ required: true });
const emit = defineEmits<{ done: [] }>();
const preset = ref(presetOf(draft.value.cron));
const save = useAction(
  () => {
    const body = scheduleBody(draft.value);
    return draft.value.id ? scheduleActions.update(draft.value.id, body) : scheduleActions.create(props.pid, { ...body, enabled: true });
  },
  { invalidate: () => [keys.schedules(props.pid)], success: "已保存", onSuccess: () => emit("done") },
);
const valid = computed(() => draftValid(draft.value));
const set = (patch: Partial<Draft>) => (draft.value = { ...draft.value, ...patch });

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
    <Field v-slot="{ id }" label="名称">
      <el-input :id="id" :model-value="draft.name" placeholder="例如：每日进展" autofocus :maxlength="100" @update:model-value="(v: string) => set({ name: v })" />
    </Field>
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="什么时候">
        <el-select :id="id" :model-value="preset" @update:model-value="choose">
          <el-option v-for="p in CRON_PRESETS" :key="p.id" :value="p.id" :label="p.label" />
          <el-option :value="CUSTOM" label="自定义（cron）" />
        </el-select>
      </Field>
      <Field v-slot="{ id }" label="cron 表达式" hint="分 时 日 月 周">
        <el-input :id="id" :model-value="draft.cron" class="at-mono" :aria-invalid="!looksLikeCron(draft.cron)" @update:model-value="typeCron" />
      </Field>
    </div>
    <Field v-slot="{ id }" label="发给统筹的消息">
      <el-input
        :id="id"
        :model-value="draft.message"
        type="textarea"
        :rows="4"
        placeholder="例如：汇总昨天各讨论串的进展，有卡住的告诉我"
        :maxlength="20000"
        @update:model-value="(v: string) => set({ message: v })"
      />
    </Field>
    <span class="flex justify-end gap-2">
      <el-button @click="emit('done')">取消</el-button>
      <el-button type="primary" native-type="submit" class="!ml-0" :disabled="!valid || save.isPending.value">保存</el-button>
    </span>
  </form>
</template>
