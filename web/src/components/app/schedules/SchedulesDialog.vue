<script setup lang="ts">
// 定时任务 dialog: messages the project coordinator gets on a schedule. `v-model:open`.
import { browserTimeZone } from "@/lib/schedules";
import SchedulesPanel from "./SchedulesPanel.vue";

defineProps<{ pid: string }>();
const open = defineModel<boolean>("open", { required: true });
</script>

<template>
  <el-dialog align-center v-model="open" class="at-dialog w-[calc(100%-2rem)] max-w-xl !rounded-[14px]" width="36rem" append-to-body>
    <template #header>
      <div class="flex flex-col gap-1.5 pr-6">
        <h2 class="text-base font-semibold">定时任务</h2>
        <p class="text-[13px] leading-5 text-muted-foreground">到点后把消息发给项目统筹，由它开讨论串去做。按你的时区（{{ browserTimeZone() }}）计算。</p>
      </div>
    </template>
    <SchedulesPanel :pid="pid" :active="open" />
  </el-dialog>
</template>
