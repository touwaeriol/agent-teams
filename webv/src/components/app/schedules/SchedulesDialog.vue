<script setup lang="ts">
// 定时任务 dialog: messages the project coordinator gets on a schedule. `v-model:open`.
import { Plus } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useSchedules } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { browserTimeZone } from "@/lib/schedules";
import ScheduleForm from "./ScheduleForm.vue";
import ScheduleRow from "./ScheduleRow.vue";
import { draftOf, emptyDraft, type Draft } from "./schedule";

const props = defineProps<{ pid: string }>();
const open = defineModel<boolean>("open", { required: true });
const list = useSchedules(() => props.pid, open);
const items = computed(() => list.data.value ?? []);
const draft = ref<Draft | null>(null);
watch(open, (o) => !o && (draft.value = null));
</script>

<template>
  <el-dialog align-center v-model="open" class="at-dialog w-[calc(100%-2rem)] max-w-xl !rounded-[14px]" width="36rem" append-to-body>
    <template #header>
      <div class="flex flex-col gap-1.5 pr-6">
        <h2 class="text-base font-semibold">定时任务</h2>
        <p class="text-[13px] leading-5 text-muted-foreground">到点后把消息发给项目统筹，由它开讨论串去做。按你的时区（{{ browserTimeZone() }}）计算。</p>
      </div>
    </template>
    <ScheduleForm v-if="draft" v-model="draft" :pid="pid" @done="draft = null" />
    <div v-else class="flex flex-col gap-2">
      <Loading v-if="list.isLoading.value" />
      <p v-if="list.data.value && items.length === 0" class="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">
        还没有定时任务。例如每天 9:00 让统筹汇报各讨论串的进展。
      </p>
      <ScheduleRow v-for="s in items" :key="s.id" :pid="pid" :item="s" @edit="draft = draftOf(s)" />
      <el-button class="mt-1 self-start" @click="draft = emptyDraft()"><Plus /> 新建定时任务</el-button>
    </div>
  </el-dialog>
</template>
