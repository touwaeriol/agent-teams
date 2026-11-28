<script setup lang="ts">
// 管理后台 → 反馈: what users sent from a thread's ⋯ → 发送反馈, newest first; 待处理 / 已处理 / 全部,
// mark handled (or back to open), and the attached conversation when the user included it.
import { $t } from "@/i18n";
import { computed, ref } from "vue";
import { feedback as feedbackActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useFeedback } from "@/api/queries";
import type { FeedbackView } from "@/api/views";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import { auditTime } from "./admin";

type Show = "open" | "handled" | "all";
const show = ref<Show>("open");
const list = useFeedback();
const items = computed(() =>
  (list.data.value ?? []).filter((f: FeedbackView) => show.value === "all" || (show.value === "open") === !f.handled_at),
);
const openCount = computed(() => (list.data.value ?? []).filter((f: FeedbackView) => !f.handled_at).length);
const expanded = ref<Record<string, boolean>>({});
const mark = useAction((v: { id: string; handled: boolean }) => feedbackActions.setHandled(v.id, v.handled), {
  invalidate: [keys.feedback],
  success: (f) => (f.handled_at ? $t("已标记为已处理") : $t("已改回待处理")),
});
const TABS: [Show, string][] = [
  ["open", $t("待处理")],
  ["handled", $t("已处理")],
  ["all", $t("全部")],
];
</script>

<template>
  <Card class="gap-3" :aria-label="$t('反馈')">
    <CardTitle>
      {{ $t("反馈") }}<span v-if="openCount" class="ml-1.5 text-[13px] font-normal text-muted-foreground">{{ $t("{openCount} 条待处理", { openCount }) }}</span>
      <template #action>
        <el-radio-group v-model="show" size="small" :aria-label="$t('显示哪些反馈')">
          <el-radio-button v-for="[id, label] in TABS" :key="id" :value="id">{{ label }}</el-radio-button>
        </el-radio-group>
      </template>
    </CardTitle>
    <p v-if="!items.length" class="text-[13px] text-muted-foreground">{{ show === "open" ? $t("没有待处理的反馈。") : $t("没有反馈。") }}</p>
    <div v-for="f in items" :key="f.id" class="flex flex-col gap-1.5 border-t border-[#F1EFEA] pt-2.5 text-[13px] dark:border-border" data-feedback="">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5">
        <span class="font-medium">{{ f.user_email || f.user_id }}</span>
        <span class="text-muted-foreground">#{{ f.thread_no }} {{ f.thread_title }}</span>
        <span class="font-mono text-xs text-muted-foreground">{{ auditTime(f.created_at) }}</span>
        <span class="flex-1" />
        <span v-if="f.handled_at" class="text-xs text-muted-foreground">{{ $t("{handled_by} 已处理", { handled_by: f.handled_by }) }}</span>
        <el-button size="small" :disabled="mark.isPending.value" @click="mark.mutate({ id: f.id, handled: !f.handled_at })">
          {{ f.handled_at ? $t("改回待处理") : $t("标记为已处理") }}
        </el-button>
      </div>
      <p class="whitespace-pre-wrap leading-5">{{ f.text }}</p>
      <template v-if="f.content">
        <button type="button" class="self-start text-xs text-muted-foreground underline-offset-2 hover:underline" @click="expanded[f.id] = !expanded[f.id]">
          {{ expanded[f.id] ? $t("收起讨论串内容") : $t("查看讨论串内容") }}
        </button>
        <pre v-if="expanded[f.id]" class="max-h-72 overflow-auto whitespace-pre-wrap rounded-md bg-raised p-2.5 font-mono text-xs leading-5">{{ f.content }}</pre>
      </template>
    </div>
  </Card>
</template>
