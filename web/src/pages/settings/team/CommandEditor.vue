<script setup lang="ts">
// 命令 on a role card: which platform commands the role's chat lists (coordinator and lead),
// and the role's own custom commands (prompt templates; passthrough for Claude Code chats).
import { Check, Pencil, Plus, X } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { RoleView } from "@/api/views";
import { commandsHint, modeLabel, promptOnly, toggleHidden, togglable, unfit, type CustomCommand } from "@/lib/commands";
import CommandDialog from "./CommandDialog.vue";
import ToggleChip from "./ToggleChip.vue";

const props = defineProps<{ role: RoleView }>();
const emit = defineEmits<{ change: [patch: Partial<RoleView>] }>();
const list = computed(() => props.role.commands ?? []);
const hidden = computed(() => props.role.hidden_commands ?? []);
const platform = computed(() => togglable(props.role));
const onlyPrompt = computed(() => promptOnly(props.role));
const bad = computed(() => unfit(props.role).map((c) => `/${c.name}`));
const editing = ref<number | null>(null);
const open = ref(false);
const current = computed(() => (editing.value === null ? null : (list.value[editing.value] ?? null)));
const taken = computed(() => list.value.filter((_, i) => i !== editing.value).map((c) => c.name));

function edit(i: number | null) {
  editing.value = i;
  open.value = true;
}
function save(c: CustomCommand) {
  const i = editing.value;
  emit("change", { commands: i === null ? [...list.value, c] : list.value.map((x, j) => (j === i ? c : x)) });
  open.value = false;
}
const remove = (i: number) => emit("change", { commands: list.value.filter((_, j) => j !== i) });
</script>

<template>
  <div class="flex flex-col gap-2 border-t border-[#F1EFEA] pt-2.5 text-[13px]" data-testid="command-editor">
    <div v-if="platform.length" class="flex flex-wrap items-center gap-1.5">
      <span class="mr-1 text-xs text-muted-foreground">平台命令</span>
      <span v-for="c in platform" :key="c.name" :title="c.desc">
        <ToggleChip :on="!hidden.includes(c.name)" @click="emit('change', { hidden_commands: toggleHidden(hidden, c.name) })">
          <Check v-if="!hidden.includes(c.name)" class="mr-0.5 inline size-3" /><span :class="hidden.includes(c.name) && 'text-muted-foreground line-through'">/{{ c.name }}</span>
        </ToggleChip>
      </span>
    </div>
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="mr-1 text-xs text-muted-foreground">自定义命令</span>
      <span v-for="(c, i) in list" :key="c.name" class="inline-flex h-6 items-center gap-1 rounded-md border border-input bg-white pl-2 pr-0.5 text-xs" :title="c.description || c.prompt">
        <span class="font-mono">/{{ c.name }}</span>
        <span class="text-muted-foreground">{{ modeLabel(c) }}</span>
        <button type="button" :aria-label="`编辑 /${c.name}`" class="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-raised" @click="edit(i)">
          <Pencil class="size-3" />
        </button>
        <button type="button" :aria-label="`删除 /${c.name}`" class="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-raised" @click="remove(i)">
          <X class="size-3" />
        </button>
      </span>
      <button type="button" class="inline-flex h-6 items-center gap-0.5 rounded-md border border-dashed border-input px-2 text-xs text-muted-foreground hover:bg-raised" @click="edit(null)">
        <Plus class="size-3" />添加命令
      </button>
    </div>
    <p class="text-xs leading-[19px] text-muted-foreground">{{ commandsHint(role) }}</p>
    <p v-if="bad.length" class="text-xs text-destructive">{{ bad.join("、") }} 是透传命令，这个岗位只能用提示词模板，请编辑或删除。</p>
    <CommandDialog v-model:open="open" :command="current" :taken="taken" :prompt-only="onlyPrompt" :codex="role.runtime === 'codex'" @save="save" />
  </div>
</template>
