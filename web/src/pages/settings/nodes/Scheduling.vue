<script setup lang="ts">
/** 调度阈值: above these used percents a node gets no new session while another node is under them. */
import { computed, ref, watch } from "vue";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useScheduling } from "@/api/queries";
import Field from "@/components/common/Field.vue";

const settings = useScheduling();
const fiveHour = ref<number | null | undefined>();
const weekly = ref<number | null | undefined>();
watch(
  () => settings.data.value,
  (s) => {
    if (!s) return;
    fiveHour.value = s.five_hour_pct;
    weekly.value = s.weekly_pct;
  },
  { immediate: true },
);
const valid = (v: number | null | undefined) => typeof v === "number" && Number.isInteger(v) && v >= 1 && v <= 100;
const dirty = computed(() => {
  const s = settings.data.value;
  return Boolean(s) && (fiveHour.value !== s?.five_hour_pct || weekly.value !== s?.weekly_pct);
});
const canSave = computed(() => dirty.value && valid(fiveHour.value) && valid(weekly.value));
const save = useAction(() => nodeActions.saveScheduling({ five_hour_pct: fiveHour.value!, weekly_pct: weekly.value! }), {
  invalidate: [keys.scheduling],
  success: "调度阈值已保存",
});
const defaults = computed(() => settings.data.value?.defaults);
</script>

<template>
  <section class="flex flex-col gap-3 rounded-xl border bg-panel px-4 py-4" data-testid="scheduling">
    <div class="flex flex-col gap-1">
      <h2 class="text-base font-semibold">调度阈值</h2>
      <p class="text-[13px] text-muted-foreground">
        新会话会在可用节点之间分摊：用量低、正在跑的会话少的节点多分。某个节点的订阅用量超过下面的阈值后，只要还有别的节点没超，就不再主动往它上面派新会话；所有节点都超了时仍会挑负载最低的一个。续接的会话（恢复原对话或已有工作区）也遵守阈值，有别的节点没超就换过去；正在跑的会话不会中途换，用量用完才迁移。
      </p>
    </div>
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="5 小时窗口超过多少后不再主动派会话" :hint="`1～100 的百分比${defaults ? `，默认 ${defaults.five_hour_pct}%` : ''}。Codex 按一天以内的窗口算。`">
        <div class="flex items-center">
          <el-input-number :id="id" v-model="fiveHour" :min="1" :max="100" :step="1" step-strictly controls-position="right" class="!w-40" data-testid="five-hour-pct" />
          <span class="ml-1.5 text-sm text-muted-foreground">%</span>
        </div>
      </Field>
      <Field v-slot="{ id }" label="周窗口超过多少后不再主动派会话" :hint="`1～100 的百分比${defaults ? `，默认 ${defaults.weekly_pct}%` : ''}。包括按模型分的周窗口；Codex 按超过一天的窗口算。`">
        <div class="flex items-center">
          <el-input-number :id="id" v-model="weekly" :min="1" :max="100" :step="1" step-strictly controls-position="right" class="!w-40" data-testid="weekly-pct" />
          <span class="ml-1.5 text-sm text-muted-foreground">%</span>
        </div>
      </Field>
    </div>
    <div>
      <el-button type="primary" :disabled="!canSave" :loading="save.isPending.value" data-testid="save-scheduling" @click="save.mutate()">保存</el-button>
    </div>
  </section>
</template>
