<script setup lang="ts">
/** The answer to a platform command, shown at the top of the message box: /model, /effort, /status, /context, /skills. */
import { useQuery } from "@tanstack/vue-query";
import { X } from "lucide-vue-next";
import { computed } from "vue";
import { projects } from "@/api/actions";
import { client, unwrap } from "@/api/client";
import { useAction } from "@/api/mutations";
import { keys, useCoordinator, useOverview, useProject } from "@/api/queries";
import type { OverviewView } from "@/api/views";
import Spinner from "@/components/common/Spinner.vue";
import { EFFORTS, modelOptions } from "@/lib/projectSettings";
import { cn } from "@/lib/utils";
import type { Scope } from "./assist";
import { contextRows, statusSummary } from "./panels";
import type { ComposerAssist, Panel } from "./useComposerAssist";

const props = defineProps<{ assist: ComposerAssist; panel: Panel; pid: string; cid: string; scope: Scope }>();
const project = useProject(() => props.pid);
const overview = useOverview(() => (props.panel === "status" ? props.pid : ""));
const coordinator = useCoordinator(() => (props.panel === "status" ? props.pid : ""));
const context = useQuery({
  queryKey: computed(() => ["c", props.cid, "context"]),
  queryFn: () => unwrap(client.GET("/api/conversations/{cid}/context", { params: { path: { cid: props.cid } } })),
  enabled: computed(() => props.panel === "context" && Boolean(props.cid)),
});
const update = useAction((body: Parameters<typeof projects.update>[1]) => projects.update(props.pid, body), {
  invalidate: () => [keys.project(props.pid)],
  success: "已保存，之后打开的会话生效",
});

const TITLES: Record<Panel, string> = { model: "模型", effort: "思考强度", status: "状态", context: "上下文", skills: "技能和插件" };
const modelKey = computed(() => (props.scope === "coordinator" ? "coordinator_model" : "thread_model"));
const model = computed(() => project.data.value?.[modelKey.value] ?? "");
const models = computed(() => modelOptions(model.value));
const effort = computed(() => project.data.value?.thread_effort ?? "");
const status = computed(() => (overview.data.value ? statusSummary(overview.data.value as unknown as OverviewView, coordinator.data.value) : null));
const rows = computed(() => (context.data.value ? contextRows(context.data.value) : []));
const skills = computed(() => {
  const d = props.assist.commands.data.value;
  return d ? { ...d, commands: d.commands ?? [] } : undefined;
});
const loading = computed(
  () => ({ model: project.isLoading, effort: project.isLoading, status: overview.isLoading, context: context.isLoading, skills: props.assist.commands.isLoading })[props.panel].value,
);
const choice = (on: boolean) =>
  cn("flex items-center justify-between rounded-lg px-3 py-2 text-left text-[13px] hover:bg-raised", on && "bg-raised font-medium");
</script>

<template>
  <section class="mx-3 mt-3 rounded-xl border bg-background p-3 text-[13px]" :aria-label="TITLES[panel]" data-testid="command-panel">
    <header class="mb-2 flex items-center justify-between">
      <span class="font-medium">/{{ panel }} · {{ TITLES[panel] }}</span>
      <button type="button" aria-label="关闭" class="flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-raised" @click="assist.closePanel()">
        <X class="size-3.5" />
      </button>
    </header>
    <Spinner v-if="loading" class="size-4" />
    <template v-else-if="panel === 'model'">
      <p class="mb-2 text-xs text-muted-foreground">
        {{ scope === "coordinator" ? "项目统筹的模型" : "这个项目所有讨论串（组长和成员）的模型" }}，对之后打开的会话生效。
      </p>
      <div class="flex flex-col gap-0.5">
        <button v-for="m in models" :key="m.value" type="button" :class="choice(m.value === model)" @click="update.mutate({ [modelKey]: m.value })">
          {{ m.label }}<span v-if="m.value === model" class="text-xs text-muted-foreground">当前</span>
        </button>
      </div>
    </template>
    <template v-else-if="panel === 'effort'">
      <p class="mb-2 text-xs text-muted-foreground">这个项目所有讨论串的思考强度，对之后打开的会话生效。</p>
      <div class="flex flex-wrap gap-1.5">
        <button
          v-for="e in EFFORTS"
          :key="e.value"
          type="button"
          :class="cn('rounded-lg border px-3 py-1.5 text-[13px] hover:bg-raised', e.value === effort && 'border-foreground/40 bg-raised font-medium')"
          @click="update.mutate({ thread_effort: e.value })"
        >
          {{ e.label }}
        </button>
      </div>
    </template>
    <template v-else-if="panel === 'status' && status">
      <dl class="grid grid-cols-3 gap-2 text-center">
        <div class="rounded-lg bg-raised py-2"><dt class="text-xs text-muted-foreground">进行中的讨论串</dt><dd class="text-base font-medium">{{ status.activeThreads }}</dd></div>
        <div class="rounded-lg bg-raised py-2"><dt class="text-xs text-muted-foreground">待你处理</dt><dd class="text-base font-medium">{{ status.cards + status.reviews }}</dd></div>
        <div class="rounded-lg bg-raised py-2"><dt class="text-xs text-muted-foreground">节点在线</dt><dd class="text-base font-medium">{{ status.nodesOnline }}/{{ status.nodesTotal }}</dd></div>
      </dl>
      <ul v-if="status.busy.length" class="mt-2 flex flex-col gap-1">
        <li v-for="b in status.busy" :key="b.where + b.name" class="flex gap-2 truncate">
          <span class="font-medium">{{ b.name }}</span><span class="text-muted-foreground">{{ b.where }}</span><span class="truncate">{{ b.doing }}</span>
        </li>
      </ul>
      <p v-else class="mt-2 text-muted-foreground">现在没有人在忙。</p>
      <p v-if="status.cards || status.reviews" class="mt-1 text-xs text-muted-foreground">卡片 {{ status.cards }} 张，待验收 {{ status.reviews }} 个。</p>
    </template>
    <dl v-else-if="panel === 'context'" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
      <template v-for="[k, v] in rows" :key="k">
        <dt class="text-muted-foreground">{{ k }}</dt>
        <dd>{{ v }}</dd>
      </template>
    </dl>
    <template v-else-if="panel === 'skills' && skills">
      <p v-if="skills.runtime === 'codex'" class="text-muted-foreground">Codex 员工没有斜杠技能。</p>
      <p v-else-if="skills.commands.length === 0" class="text-muted-foreground">
        {{ skills.tier === "coordinator" ? "项目统筹不带技能和插件。" : `${skills.employee} 的岗位没有配置技能或插件。` }}
      </p>
      <template v-else>
        <p class="mb-1.5 text-xs text-muted-foreground">
          {{ skills.from === "session" ? "当前会话加载的" : "岗位配置的（会话打开后以会话为准）" }}，输入 /名字 让 {{ skills.employee }} 使用。
        </p>
        <ul class="flex flex-col gap-1">
          <li v-for="c in skills.commands" :key="c.name" class="flex gap-2">
            <span class="shrink-0 font-mono">/{{ c.name }}</span>
            <span class="truncate text-muted-foreground">{{ c.description || (c.source === "plugin" ? "插件命令" : "技能") }}</span>
          </li>
        </ul>
      </template>
    </template>
  </section>
</template>
