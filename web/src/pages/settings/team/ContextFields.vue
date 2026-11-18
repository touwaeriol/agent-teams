<script setup lang="ts">
// 会话管理: the role's context limit and when its sessions compact or start fresh (shared-members.md §5.1).
// The percentages are of the role's own limit, not of the model's window.
import { computed } from "vue";
import type { RoleView } from "@/api/views";
import Field from "@/components/common/Field.vue";
import { contextDefaults, contextTokens } from "@/lib/roles";

const d = defineModel<RoleView>({ required: true });
const member = computed(() => d.value.tier === "member");
const num = (v: string) => (v.trim() === "" ? null : Number(v));
const text = (v: number | null | undefined) => (v == null ? "" : String(v));
const at = (pct: number | null | undefined) => {
  const t = contextTokens(d.value.context_limit, pct);
  return t ? `，即 ${t}` : "";
};
const reset = () => Object.assign(d.value, contextDefaults(d.value));
</script>

<template>
  <section class="flex flex-col gap-3 rounded-lg border px-3 py-3" aria-label="会话管理">
    <div class="flex items-center justify-between gap-3">
      <span class="text-[13px] font-medium">会话管理（上下文和压缩）</span>
      <el-button size="small" text @click="reset">按岗位类型恢复默认</el-button>
    </div>
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" label="上下文上限（token）" hint="例如 600000。下面的比例都相对它，不是模型本身的窗口：1M 窗口的模型设 600K，60% 就是 360K。留空 = 模型窗口">
        <el-input :id="id" type="number" :min="10000" :step="10000" placeholder="模型窗口" :model-value="text(d.context_limit)" @update:model-value="(v: string) => (d.context_limit = num(v))" />
      </Field>
      <Field v-slot="{ id }" label="任务中自动压缩（%）" :hint="`做任务时用到上限的这个比例${at(d.compact_in_task_pct)}，运行时自己压缩。留空 = 运行时默认`">
        <el-input :id="id" type="number" :min="1" :max="100" placeholder="运行时默认" :model-value="text(d.compact_in_task_pct)" @update:model-value="(v: string) => (d.compact_in_task_pct = num(v))" />
      </Field>
      <Field v-if="member" v-slot="{ id }" label="领任务时压缩（%）" :hint="`领下一个任务时已用 ≥ 上限的这个比例${at(d.compact_between_pct)}，先压缩再发任务。留空 = 不压缩`">
        <el-input :id="id" type="number" :min="1" :max="100" placeholder="不压缩" :model-value="text(d.compact_between_pct)" @update:model-value="(v: string) => (d.compact_between_pct = num(v))" />
      </Field>
      <Field v-if="member" v-slot="{ id }" label="连续几个任务后换新会话" hint="同一讨论串里一个会话做满这么多任务就关掉、开新会话（审查岗建议 1）。0 = 不按次数">
        <el-input :id="id" type="number" :min="0" placeholder="0" :model-value="text(d.clear_after_tasks)" @update:model-value="(v: string) => (d.clear_after_tasks = num(v))" />
      </Field>
    </div>
    <Field v-slot="{ id }" label="压缩时要保留什么" hint="拼进压缩指令，例如任务 id、分支、改过的文件、没做完的事">
      <el-input :id="id" v-model="d.compact_focus" type="textarea" :rows="2" />
    </Field>
  </section>
</template>
