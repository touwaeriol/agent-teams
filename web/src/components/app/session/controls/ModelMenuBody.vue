<script setup lang="ts">
/**
 * Model · 思考强度: the models the session's CLI listed (each with its effort levels) and the
 * effort levels of the current model as chips. A runtime without a separate effort (Antigravity)
 * says the effort is part of the model id. Without a list (no session yet, older node) only the
 * current model shows.
 */
import { Check } from "lucide-vue-next";
import { computed } from "vue";
import { effortLabel, type ModelChoice } from "@/lib/sessionControls";

const props = defineProps<{ models: ModelChoice[]; model: string; effort: string; efforts: string[]; runtime: string; disabled?: boolean; note?: string; touch?: boolean }>();
const emit = defineEmits<{ model: [id: string]; effort: [level: string] }>();
const rows = computed(() => (props.models.length ? props.models : props.model ? [{ id: props.model, efforts: [], supports_auto: false } as ModelChoice] : []));
function sub(m: ModelChoice): string {
  if (props.runtime === "antigravity") return m.description ?? "";
  const e = m.efforts ?? [];
  const parts = [e.length ? `思考强度：${e.map(effortLabel).join(" / ")}` : ""];
  if (props.runtime === "claude" && props.models.length && !m.supports_auto) parts.push("不支持自动模式");
  return parts.filter(Boolean).join(" · ");
}
</script>

<template>
  <div class="flex flex-col text-[13px]" data-testid="model-menu">
    <div class="px-2.5 pb-1 pt-0.5 text-xs font-semibold text-foreground">模型</div>
    <div class="scroll-thin flex max-h-[min(40vh,320px)] flex-col overflow-y-auto">
      <button
        v-for="m in rows"
        :key="m.id"
        type="button"
        role="menuitemradio"
        :aria-checked="m.id === model"
        :disabled="disabled"
        :data-model="m.id"
        :class="['flex w-full items-start gap-2 rounded-lg px-2.5 py-1.5 text-left', touch ? 'py-2.5' : '', disabled ? 'cursor-default opacity-70' : 'hover:bg-accent']"
        @click="m.id !== model && emit('model', m.id)"
      >
        <span class="flex min-w-0 flex-1 flex-col leading-5">
          <span class="truncate font-medium">{{ m.name || m.id }}</span>
          <span v-if="sub(m)" class="text-xs text-muted-foreground">{{ sub(m) }}</span>
        </span>
        <Check v-if="m.id === model" class="mt-0.5 size-3.5 shrink-0" />
      </button>
      <p v-if="!models.length" class="px-2.5 py-1 text-xs text-muted-foreground">会话在节点上打开后才能列出可选的模型。</p>
    </div>
    <div class="mt-1 border-t px-2.5 pb-1 pt-2 text-xs font-semibold text-foreground">思考强度</div>
    <p v-if="runtime === 'antigravity'" class="px-2.5 pb-1 text-xs text-muted-foreground">强度是模型的一部分：在上面选带 high / xhigh 的模型即可。</p>
    <div v-else-if="efforts.length" class="flex flex-wrap gap-1 px-2.5 pb-1.5" data-testid="effort-chips">
      <button
        v-for="e in efforts"
        :key="e"
        type="button"
        :aria-pressed="e === effort"
        :disabled="disabled"
        :data-effort="e"
        :class="['rounded-full border px-2.5 py-0.5 text-xs', e === effort ? 'border-foreground/60 bg-foreground text-background' : 'hover:bg-accent', disabled ? 'opacity-70' : '']"
        @click="e !== effort && emit('effort', e)"
      >{{ effortLabel(e) }}</button>
    </div>
    <p v-else class="px-2.5 pb-1 text-xs text-muted-foreground">这个模型没有可选的思考强度。</p>
    <p class="mt-1 border-t px-2.5 pt-1.5 text-[11.5px] leading-[18px] text-muted-foreground">{{ note || "模型和思考强度只改这个会话，不改岗位设置。" }}</p>
  </div>
</template>
