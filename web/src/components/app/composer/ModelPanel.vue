<script setup lang="ts">
/**
 * /model: the models of the employee's runtime (current one marked), a typed model id, and in a
 * thread whether only the lead or the whole thread switches. A running session switches from
 * its next turn; otherwise the next session opens with the model.
 */
import { computed, ref } from "vue";
import { models as modelActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useConversationModel, useModels } from "@/api/queries";
import Spinner from "@/components/common/Spinner.vue";
import { MODEL_ID, modelChoices, modelName, runtimeLabel, type Runtime } from "@/lib/models";
import { cn } from "@/lib/utils";
import type { Scope } from "./assist";
import { modelAppliedText } from "./panels";

const props = defineProps<{ pid: string; cid: string; scope: Scope }>();
const state = useConversationModel(() => props.cid);
const models = useModels();
const whole = ref(false);
const typed = ref("");
const list = computed(() => models.data.value?.models ?? []);
const s = computed(() => state.data.value);
const runtime = computed(() => (s.value?.runtime ?? "claude") as Runtime);
const choices = computed(() => modelChoices(list.value, runtime.value, s.value?.model ?? ""));
const name = (id: string) => modelName(list.value, id);
const others = computed(() => (s.value?.members ?? []).filter((m) => m.runtime !== runtime.value).map((m) => m.name));
const typedOk = computed(() => MODEL_ID.test(typed.value.trim()));
const set = useAction((model: string) => modelActions.setConversation(props.cid, model, whole.value ? "thread" : "lead"), {
  invalidate: () => [keys.conversationModel(props.cid), keys.project(props.pid)],
  success: (r) => modelAppliedText(r, name),
  onSuccess: () => (typed.value = ""),
});
const live = computed(() => {
  if (!s.value?.live) return "现在没有运行中的会话：选好后，下次打开会话时使用。";
  const last = s.value.session ? `上一轮用的是 ${name(s.value.session)}，` : "";
  return `会话运行中，${last}切换后从下一轮起生效。`;
});
const row = (on: boolean) => cn("flex items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-[13px] hover:bg-raised", on && "bg-raised font-medium");
const seg = (on: boolean) => cn("rounded-md px-2.5 py-1 text-xs", on ? "bg-background font-medium shadow-sm" : "text-muted-foreground");
</script>

<template>
  <Spinner v-if="!s || models.isLoading.value" class="size-4" />
  <div v-else class="flex flex-col gap-2" data-testid="model-panel">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="text-xs text-muted-foreground">
        {{ s.scope === "coordinator" ? "项目统筹" : whole ? "这个讨论串的员工" : `组长 ${s.employee} ` }}用的模型 · {{ runtimeLabel(s.runtime) }}
      </p>
      <div v-if="s.scope === 'thread'" class="flex rounded-lg bg-raised p-0.5" role="radiogroup" aria-label="切换范围">
        <button type="button" role="radio" :aria-checked="!whole" :class="seg(!whole)" @click="whole = false">仅组长</button>
        <button type="button" role="radio" :aria-checked="whole" :class="seg(whole)" @click="whole = true">整个讨论串</button>
      </div>
    </div>
    <p class="text-xs text-muted-foreground">
      {{ live }}<template v-if="whole && others.length">{{ others.join("、") }} 不是 {{ runtimeLabel(s.runtime) }}，不受影响。</template>
    </p>
    <div class="flex max-h-[220px] flex-col gap-0.5 overflow-y-auto">
      <button v-if="s.override" type="button" :class="row(false)" :disabled="set.isPending.value" @click="set.mutate('')">
        <span>跟随设置<span class="ml-2 text-xs text-muted-foreground">{{ name(s.inherited) }}</span></span>
      </button>
      <button v-for="c in choices" :key="c.value" type="button" :class="row(c.value === s.model)" :disabled="set.isPending.value" @click="set.mutate(c.value)">
        <span class="min-w-0 truncate">
          {{ c.label }}<span v-if="c.label !== c.value" class="ml-2 font-mono text-[11.5px] text-muted-foreground">{{ c.value }}</span>
        </span>
        <span class="flex shrink-0 gap-1.5 text-xs text-muted-foreground">
          <span v-if="c.isDefault">默认</span><span v-if="c.value === s.model" class="text-foreground">当前</span>
        </span>
      </button>
    </div>
    <form class="flex gap-2" @submit.prevent="typedOk && set.mutate(typed.trim())">
      <el-input v-model="typed" size="small" placeholder="输入其他模型 ID" aria-label="模型 ID" class="at-mono" />
      <el-button size="small" native-type="submit" :disabled="!typedOk || set.isPending.value">使用</el-button>
    </form>
  </div>
</template>
