<script setup lang="ts">
// The flow chart editor: steps are boxes, edges are arrows. Drag a box to move it, drag from its
// right dot to another box to connect them (然后); click a box or an arrow to edit it on the
// right. Several arrows out of one step run in parallel; several into one step wait for all.
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import { MarkerType, VueFlow, type Connection, type Edge, type Node, type NodeDragEvent } from "@vue-flow/core";
import { Plus, Trash2, Wand2 } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { FlowDraft, FlowEdge, FlowStep } from "@/api/views";
import Field from "@/components/common/Field.vue";
import { EDGE_KINDS, STEP_KINDS, addEdge, edgeKey, kindLabel, layout, newStepId, removeStep, roleLabel, type EdgeKind, type RoleChoice, type StepKind } from "./flow";
import StepNode from "./StepNode.vue";

const props = defineProps<{ modelValue: FlowDraft; roles: RoleChoice[] }>();
const emit = defineEmits<{ "update:modelValue": [d: FlowDraft] }>();
const selected = ref<{ kind: "step"; id: string } | { kind: "edge"; key: string } | null>(null);

const steps = computed(() => props.modelValue.steps ?? []);
const edges = computed(() => props.modelValue.edges ?? []);
const TONE: Record<string, string> = {
  work: "bg-sky-500/10 text-sky-700 dark:text-sky-300",
  review: "bg-amber-500/15 text-amber-700 dark:text-amber-300",
  decision: "bg-violet-500/15 text-violet-700 dark:text-violet-300",
  approval: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
};

const known = computed(() => new Set(props.roles.map((r) => r.value)));
const nodes = computed<Node[]>(() =>
  steps.value.map((s) => ({
    id: s.id,
    type: "step",
    position: { x: s.x ?? 0, y: s.y ?? 0 },
    selected: selected.value?.kind === "step" && selected.value.id === s.id,
    data: { name: s.name || "未命名", who: roleLabel(s.role, props.roles), kind: kindLabel(s.kind), tone: TONE[s.kind ?? "work"], missing: !!s.role && !known.value.has(s.role) },
  })),
);
const flowEdges = computed<Edge[]>(() =>
  edges.value.map((e) => {
    const reject = e.kind === "reject";
    const on = selected.value?.kind === "edge" && selected.value.key === edgeKey(e);
    const color = on ? "#d97706" : reject ? "#c2410c" : "#8a8780";
    return {
      id: edgeKey(e),
      source: e.from,
      target: e.to,
      type: reject ? "smoothstep" : "default",
      label: e.label || (reject ? "退回" : ""),
      animated: false,
      selected: on,
      markerEnd: { type: MarkerType.ArrowClosed, color },
      style: { stroke: color, strokeWidth: on ? 2.6 : 1.6, ...(reject ? { strokeDasharray: "6 4" } : {}) },
      labelStyle: { fill: color, fontSize: "12px" },
    };
  }),
);

const step = computed(() => (selected.value?.kind === "step" ? steps.value.find((s) => s.id === (selected.value as { id: string }).id) : undefined));
const edge = computed(() => (selected.value?.kind === "edge" ? edges.value.find((e) => edgeKey(e) === (selected.value as { key: string }).key) : undefined));

function put(patch: Partial<FlowDraft>) {
  emit("update:modelValue", { ...props.modelValue, ...patch });
}

function patchStep(id: string, patch: Partial<FlowStep>) {
  put({ steps: steps.value.map((s) => (s.id === id ? { ...s, ...patch } : s)) });
}

function patchEdge(key: string, patch: Partial<FlowEdge>) {
  const old = edges.value.find((e) => edgeKey(e) === key);
  if (!old) return;
  const changed = { ...old, ...patch };
  if (edgeKey(changed) !== key && edges.value.some((e) => edgeKey(e) === edgeKey(changed))) return; // would duplicate another arrow
  put({ edges: edges.value.map((e) => (edgeKey(e) === key ? changed : e)) });
  selected.value = { kind: "edge", key: edgeKey(changed) };
}

function addStep() {
  const id = newStepId(steps.value);
  const right = Math.max(-240, ...steps.value.map((s) => s.x ?? 0)) + 240;
  const first = props.roles[0]?.value ?? "lead";
  put({ steps: [...steps.value, { id, name: "新步骤", role: first, kind: "work", instructions: "", x: right, y: 0 }] });
  selected.value = { kind: "step", id };
}

function deleteSelected() {
  if (selected.value?.kind === "step") put(removeStep(props.modelValue, selected.value.id));
  if (selected.value?.kind === "edge") {
    const key = selected.value.key;
    put({ edges: edges.value.filter((e) => edgeKey(e) !== key) });
  }
  selected.value = null;
}

function onConnect(c: Connection) {
  put({ edges: addEdge(edges.value, { from: c.source, to: c.target, kind: "next" }) });
}

function onDragStop(e: NodeDragEvent) {
  const moved = new Map(e.nodes.map((n) => [n.id, n.position]));
  put({ steps: steps.value.map((s) => (moved.has(s.id) ? { ...s, x: Math.round(moved.get(s.id)!.x), y: Math.round(moved.get(s.id)!.y) } : s)) });
}

function tidy() {
  put({ steps: layout(steps.value, edges.value) });
}

function setKind(id: string, kind: StepKind) {
  patchStep(id, kind === "approval" ? { kind, role: "" } : { kind, role: step.value?.role || props.roles[0]?.value || "lead" });
}

const stepName = (id: string) => steps.value.find((s) => s.id === id)?.name ?? id;
</script>

<template>
  <div class="flex flex-col gap-3 lg:flex-row">
    <div class="relative h-[460px] min-w-0 flex-1 overflow-hidden rounded-xl border bg-background sm:h-[540px]">
      <div class="absolute left-3 top-3 z-10 flex gap-2">
        <el-button size="small" @click="addStep"><Plus class="mr-1 size-3.5" />添加步骤</el-button>
        <el-button size="small" @click="tidy"><Wand2 class="mr-1 size-3.5" />自动排列</el-button>
      </div>
      <VueFlow
        :nodes="nodes"
        :edges="flowEdges"
        :default-viewport="{ zoom: 0.9, x: 40, y: 70 }"
        :min-zoom="0.3"
        :max-zoom="1.6"
        :delete-key-code="null"
        fit-view-on-init
        @connect="onConnect"
        @node-drag-stop="onDragStop"
        @node-click="(e) => (selected = { kind: 'step', id: e.node.id })"
        @edge-click="(e) => (selected = { kind: 'edge', key: e.edge.id })"
        @pane-click="selected = null"
      >
        <template #node-step="p"><StepNode :data="p.data" :selected="p.selected" /></template>
        <Background :gap="18" />
        <Controls :show-interactive="false" position="bottom-left" />
      </VueFlow>
    </div>

    <aside class="flex w-full shrink-0 flex-col gap-3 rounded-xl border bg-panel p-4 lg:w-[300px]">
      <template v-if="step">
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold">步骤</span>
          <el-button text size="small" type="danger" @click="deleteSelected"><Trash2 class="mr-1 size-3.5" />删除步骤</el-button>
        </div>
        <Field v-slot="{ id }" label="名称">
          <el-input :id="id" :model-value="step.name" maxlength="40" @update:model-value="(v: string) => patchStep(step!.id, { name: v })" />
        </Field>
        <Field label="类型">
          <el-radio-group :model-value="step.kind ?? 'work'" size="small" @update:model-value="(v) => setKind(step!.id, v as StepKind)">
            <el-radio-button v-for="k in STEP_KINDS" :key="k.value" :value="k.value">{{ k.label }}</el-radio-button>
          </el-radio-group>
          <template #hint>{{ STEP_KINDS.find((k) => k.value === (step!.kind ?? "work"))?.hint }}</template>
        </Field>
        <Field v-if="step.kind !== 'approval'" v-slot="{ id }" label="谁来做">
          <el-select :id="id" :model-value="step.role" @update:model-value="(v: string) => patchStep(step!.id, { role: v })">
            <el-option v-for="r in roles" :key="r.value" :value="r.value" :label="r.label" />
          </el-select>
        </Field>
        <Field v-slot="{ id }" label="说明和验收标准" hint="组长拆任务时会把它写进这一步的任务里。">
          <el-input
            :id="id"
            type="textarea"
            :rows="5"
            maxlength="2000"
            :model-value="step.instructions ?? ''"
            @update:model-value="(v: string) => patchStep(step!.id, { instructions: v })"
          />
        </Field>
      </template>
      <template v-else-if="edge">
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold">{{ stepName(edge.from) }} → {{ stepName(edge.to) }}</span>
          <el-button text size="small" type="danger" @click="deleteSelected"><Trash2 class="mr-1 size-3.5" />删除连线</el-button>
        </div>
        <Field label="类型">
          <el-radio-group :model-value="edge.kind ?? 'next'" size="small" @update:model-value="(v) => patchEdge(edgeKey(edge!), { kind: v as EdgeKind })">
            <el-radio-button v-for="k in EDGE_KINDS" :key="k.value" :value="k.value">{{ k.label }}</el-radio-button>
          </el-radio-group>
          <template #hint>“不通过退回”：前一步审查或决策没通过时，回到箭头指向的步骤返工。</template>
        </Field>
        <Field v-slot="{ id }" label="线上的文字（可选）">
          <el-input :id="id" :model-value="edge.label ?? ''" maxlength="20" placeholder="比如：有问题" @update:model-value="(v: string) => patchEdge(edgeKey(edge!), { label: v })" />
        </Field>
      </template>
      <div v-else class="flex flex-col gap-2 text-[13px] leading-5 text-muted-foreground">
        <span class="text-sm font-semibold text-foreground">怎么画</span>
        <span>点“添加步骤”加一个方框，给它选岗位和类型。</span>
        <span>从方框右边的圆点拖到另一个方框，就连上一条“然后”的箭头；点箭头可以改成“不通过退回”。</span>
        <span>一个步骤连出几条箭头，这几步会并行做；几条箭头指向同一步，它要等前面都完成。</span>
        <span>组长拆任务时会参考这张图，遇到特殊情况可以调整，并会说明原因。</span>
      </div>
    </aside>
  </div>
</template>
