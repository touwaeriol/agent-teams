<script setup lang="ts">
// The 定时任务 list with 新建 / 编辑 in place: the body of the dialog and of the phone overview's 定时任务 tab.
// Active schedules come first, then the ones that ended in the last 7 days.
import { $t } from "@/i18n";
import { Plus } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useSchedules } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import ScheduleForm from "./ScheduleForm.vue";
import ScheduleRow from "./ScheduleRow.vue";
import { draftOf, emptyDraft, type Draft } from "./schedule";

const props = withDefaults(defineProps<{ pid: string; active?: boolean }>(), { active: true });
const list = useSchedules(() => props.pid, () => props.active);
const items = computed(() => list.data.value ?? []);
const draft = ref<Draft | null>(null);
watch(() => props.active, (a) => !a && (draft.value = null));
</script>

<template>
  <ScheduleForm v-if="draft" v-model="draft" :pid="pid" @done="draft = null" />
  <div v-else class="flex flex-col gap-2">
    <Loading v-if="list.isLoading.value" />
    <p v-if="list.data.value && items.length === 0" class="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">
      {{ $t("还没有定时任务。例如每天 9:00 让统筹汇报各讨论串的进展。") }}
    </p>
    <ScheduleRow v-for="s in items" :key="s.id" :pid="pid" :item="s" @edit="draft = draftOf(s)" />
    <el-button class="mt-1 self-start" @click="draft = emptyDraft()"><Plus /> {{ $t("新建定时任务") }}</el-button>
  </div>
</template>
