<script setup lang="ts">
// 定时任务 dialog: messages the coordinator or a thread's lead gets on a schedule, the user's and
// the ones agents set with schedule_create. `v-model:open`.
import { $t } from "@/i18n";
import { browserTimeZone } from "@/lib/schedules";
import SchedulesPanel from "./SchedulesPanel.vue";

defineProps<{ pid: string }>();
const open = defineModel<boolean>("open", { required: true });
</script>

<template>
  <el-dialog align-center v-model="open" class="at-dialog w-[calc(100%-2rem)] max-w-xl !rounded-[14px]" width="36rem" append-to-body>
    <template #header>
      <div class="flex flex-col gap-1.5 pr-6">
        <h2 class="text-base font-semibold">{{ $t("定时任务") }}</h2>
        <p class="text-[13px] leading-5 text-muted-foreground">{{ $t("到点后把消息发给项目统筹或某个讨论串的组长。统筹和组长自己设置的定时唤醒也在这里，可以停用或删除。按你的时区（{browserTimeZone}）计算。", { browserTimeZone: browserTimeZone() }) }}</p>
      </div>
    </template>
    <SchedulesPanel :pid="pid" :active="open" />
  </el-dialog>
</template>
