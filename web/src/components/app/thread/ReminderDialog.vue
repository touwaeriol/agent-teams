<script setup lang="ts">
import { $t } from "@/i18n";
/** 添加提醒 → 自定义…: pick a date and time (local, in the future, within a year). */
import { computed, ref } from "vue";
import type { ThreadView } from "@/api/views";
import FormDialog from "@/components/common/FormDialog.vue";
import { reminderPresets } from "@/lib/threadList";

const props = defineProps<{ thread: ThreadView }>();
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ pick: [at: Date] }>();
const tomorrow = reminderPresets(new Date()).find((p) => p.id === "tomorrow")!.at;
const at = ref<Date | null>(props.thread.remind_at ? new Date(props.thread.remind_at) : tomorrow);
const YEAR = 366 * 86_400_000;
const valid = computed(() => at.value !== null && at.value.getTime() > Date.now() && at.value.getTime() - Date.now() < YEAR);
const disabledDate = (d: Date) => d.getTime() < new Date().setHours(0, 0, 0, 0) || d.getTime() - Date.now() > YEAR;
function submit() {
  if (!valid.value || !at.value) return;
  emit("pick", at.value);
  open.value = false;
}
</script>

<template>
  <FormDialog v-model:open="open" :title="$t('提醒 #{no}', { no: thread.no })" :description="$t('到时间后讨论串会移到“等你处理”，浏览器允许时还会弹出通知。')" :submit-label="$t('设置提醒')" :disabled="!valid" width="26rem" @submit="submit">
    <el-date-picker
      v-model="at"
      type="datetime"
      format="YYYY-MM-DD HH:mm"
      :disabled-date="disabledDate"
      :clearable="false"
      :placeholder="$t('选择日期和时间')"
      :aria-label="$t('提醒时间')"
      class="!w-full"
    />
    <p v-if="at && !valid" class="text-xs text-destructive">{{ $t("提醒时间要在将来一年以内。") }}</p>
  </FormDialog>
</template>
