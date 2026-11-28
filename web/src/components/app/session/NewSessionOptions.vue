<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * The row under the new-session box: where it runs (节点: 自动选择 or one of the user's nodes, with
 * whether each is online), who does it (岗位: a member role used only as a template), the agent
 * (the CLI among the role's runtimes; a plain tag when the role has one) and the model (跟随岗位 or
 * one typed). More controls (permission mode, effort) go into the default slot at the end of the
 * row; they get the picked agent. Node, role and agent are fixed once the session starts.
 */
import { Bot, Check, ChevronDown, Cpu, Server, Sparkles, Terminal } from "lucide-vue-next";
import { computed } from "vue";
import type { NodeView, RoleView } from "@/api/views";
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import { runtimesOf, scopedModel, type Runtime } from "@/lib/models";
import { INK } from "@/lib/present";
import { nodeAgentProblem } from "./session";
import { runtimeLabel } from "@/lib/runtimes";

const props = defineProps<{ nodes: NodeView[]; roles: RoleView[]; auto: string }>();
const node = defineModel<string>("node", { required: true });
const role = defineModel<string>("role", { required: true });
const model = defineModel<string>("model", { required: true });
/** The picked agent (runtime); the parent keeps it one of the role's runtimes. */
const agent = defineModel<string>("agent", { required: true });
const pickedNode = computed(() => props.nodes.find((n) => n.id === node.value));
const nodeLabel = computed(() => (node.value === props.auto || !pickedNode.value ? $t("自动选择节点") : pickedNode.value.name));
const pickedRole = computed(() => props.roles.find((r) => r.role === role.value));
const roleLabel = computed(() => pickedRole.value?.title || pickedRole.value?.role || $t("选择岗位"));
const agents = computed(() => (pickedRole.value ? runtimesOf(pickedRole.value) : []));
const roleModel = computed(() => (pickedRole.value ? (scopedModel(pickedRole.value, agent.value) ?? "") : ""));
const modelLabel = computed(() => model.value.trim() || roleModel.value || $t("跟随岗位模型"));
/** Why the node cannot take a session of the picked agent ("" = it can): offline, CLI missing or not logged in (runtime_status). */
const problem = (n: NodeView) => nodeAgentProblem(n as never, agent.value, runtimeLabel);
const roleAgents = (r: RoleView) => runtimesOf(r).map(runtimeLabel).join(" / ");
const chip = "inline-flex h-7 max-w-[240px] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground data-[open=true]:bg-accent";
const item = "flex w-full min-w-[220px] items-start gap-2 py-0.5";
</script>

<template>
  <div class="flex flex-wrap items-center gap-1" data-testid="new-session-options">
    <el-dropdown trigger="click" placement="top-start" popper-class="at-session-menu" @command="(v: string) => (node = v)">
      <button type="button" :class="chip" data-testid="node-chip" :title="$t('节点：{nodeLabel}', { nodeLabel })">
        <Server class="size-3.5 shrink-0" />
        <span v-if="pickedNode" class="size-1.5 shrink-0 rounded-full" :style="{ background: pickedNode.online ? INK.done : INK.idle }" />
        <span class="truncate">{{ nodeLabel }}</span>
        <ChevronDown class="size-3 shrink-0" />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item :command="auto">
            <span :class="item">
              <Sparkles class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              <span class="flex min-w-0 flex-1 flex-col leading-5">
                <span>{{ $t("自动选择") }}</span>
                <span class="text-xs text-muted-foreground">{{ $t("按用量和会话数在在线节点间分摊") }}</span>
              </span>
              <Check v-if="node === auto || !pickedNode" class="mt-0.5 size-3.5 shrink-0" />
            </span>
          </el-dropdown-item>
          <el-dropdown-item v-for="n in nodes" :key="n.id" :command="n.id" :disabled="Boolean(problem(n))" :divided="n === nodes[0]">
            <span :class="item" :data-node="n.name" :data-problem="problem(n)">
              <span class="mt-[7px] size-2 shrink-0 rounded-full" :style="{ background: n.online ? INK.done : INK.idle }" />
              <span class="flex min-w-0 flex-1 flex-col leading-5">
                <span class="truncate">{{ n.name }}</span>
                <span class="text-xs text-muted-foreground">{{ problem(n) || $t("在线") }}</span>
              </span>
              <Check v-if="node === n.id" class="mt-0.5 size-3.5 shrink-0" />
            </span>
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <el-dropdown trigger="click" placement="top-start" popper-class="at-session-menu" @command="(v: string) => (role = v)">
      <button type="button" :class="chip" data-testid="role-chip" :title="$t('岗位：{roleLabel}', { roleLabel })">
        <Bot class="size-3.5 shrink-0" />
        <span class="truncate">{{ roleLabel }}</span>
        <ChevronDown class="size-3 shrink-0" />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <li class="max-w-[260px] px-4 pb-1.5 pt-1 text-xs leading-5 text-muted-foreground">{{ $t("岗位只当模板（提示词、CLI、模型、权限），不占岗位编制和节点上的岗位名额") }}</li>
          <el-dropdown-item v-for="r in roles" :key="r.role" :command="r.role">
            <span :class="item">
              <span class="flex min-w-0 flex-1 flex-col leading-5">
                <span class="truncate">{{ r.title || r.role }}</span>
                <span class="text-xs text-muted-foreground">{{ roleAgents(r) }}{{ r.model ? ` · ${r.model}` : "" }}</span>
              </span>
              <Check v-if="role === r.role" class="mt-0.5 size-3.5 shrink-0" />
            </span>
          </el-dropdown-item>
          <li v-if="roles.length === 0" class="px-4 py-1.5 text-xs text-muted-foreground">{{ $t("团队里还没有成员岗位") }}</li>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <el-dropdown v-if="agents.length > 1" trigger="click" placement="top-start" popper-class="at-session-menu" @command="(v: string) => (agent = v)">
      <button type="button" :class="chip" data-testid="agent-chip" :title="`Agent：${runtimeLabel(agent)}`">
        <Terminal class="size-3.5 shrink-0" />
        <span class="truncate">{{ runtimeLabel(agent) }}</span>
        <ChevronDown class="size-3 shrink-0" />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <li class="max-w-[260px] px-4 pb-1.5 pt-1 text-xs leading-5 text-muted-foreground">{{ $t("这个岗位配置的 agent；会话开始后不能再换") }}</li>
          <el-dropdown-item v-for="rt in agents" :key="rt" :command="rt">
            <span :class="item" :data-agent="rt">
              <span class="flex min-w-0 flex-1 flex-col leading-5">
                <span class="truncate">{{ runtimeLabel(rt) }}</span>
                <span class="text-xs text-muted-foreground">{{ (pickedRole && scopedModel(pickedRole, rt)) || $t("默认模型") }}</span>
              </span>
              <Check v-if="agent === rt" class="mt-0.5 size-3.5 shrink-0" />
            </span>
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <span v-else-if="agents.length === 1" :class="[chip, 'cursor-default hover:bg-transparent hover:text-muted-foreground']" data-testid="agent-chip" :title="$t('这个岗位只配置了这一个 agent')">
      <Terminal class="size-3.5 shrink-0" />
      <span class="truncate">{{ runtimeLabel(agent) }}</span>
    </span>
    <el-popover trigger="click" placement="top-start" :width="300">
      <template #reference>
        <button type="button" :class="chip" data-testid="model-chip" :title="$t('模型：{modelLabel}', { modelLabel })">
          <Cpu class="size-3.5 shrink-0" />
          <span class="truncate">{{ modelLabel }}</span>
          <ChevronDown class="size-3 shrink-0" />
        </button>
      </template>
      <div class="flex flex-col gap-2">
        <span class="text-xs text-muted-foreground">{{ $t("模型（{agent}）", { agent: runtimeLabel(agent) }) }}</span>
        <ModelPicker v-model="model" live :runtime="agent as Runtime" :label="$t('模型')" :placeholder="roleModel ? $t('跟随岗位：{roleModel}', { roleModel }) : $t('跟随岗位配置')" size="small" />
      </div>
    </el-popover>
    <!-- Permission mode and effort go here (another task). -->
    <slot :runtime="agent" :role="pickedRole" />
  </div>
</template>
