<script setup lang="ts">
import { $t } from "@/i18n";
/** 内置命令: custom commands every role gets (a role's own command of the same name wins). */
import { Pencil, Plus, X } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useBuiltinCommands } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import Spinner from "@/components/common/Spinner.vue";
import { modeLabel, type CustomCommand } from "@/lib/commands";
import CommandDialog from "@/pages/settings/team/CommandDialog.vue";

const saved = useBuiltinCommands();
const draft = ref<CustomCommand[]>([]);
watch(() => saved.data.value?.commands, (c) => (draft.value = [...(c ?? [])]), { immediate: true });
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(saved.data.value?.commands ?? []));
const editing = ref<number | null>(null);
const open = ref(false);
const current = computed(() => (editing.value === null ? null : (draft.value[editing.value] ?? null)));
const taken = computed(() => draft.value.filter((_, i) => i !== editing.value).map((c) => c.name));
const save = useAction(() => admin.saveCommands(draft.value), { invalidate: [keys.builtinCommands], success: $t("内置命令已保存") });

function edit(i: number | null) {
  editing.value = i;
  open.value = true;
}
function put(c: CustomCommand) {
  const i = editing.value;
  draft.value = i === null ? [...draft.value, c] : draft.value.map((x, j) => (j === i ? c : x));
  open.value = false;
}
</script>

<template>
  <Card class="gap-3">
    <div class="flex flex-col gap-1">
      <span class="font-semibold">{{ $t("内置命令") }}</span>
      <span class="text-xs leading-5 text-muted-foreground">{{ $t("每个岗位的聊天框都会列出这些自定义命令；岗位自己有同名命令时用岗位的。透传命令不会给 Codex、Antigravity 岗位和成员。") }}</span>
    </div>
    <div class="flex flex-wrap items-center gap-1.5 text-[13px]">
      <span v-for="(c, i) in draft" :key="c.name" class="inline-flex h-6 items-center gap-1 rounded-md border border-input bg-card pl-2 pr-0.5 text-xs" :title="c.description || c.prompt">
        <span class="font-mono">/{{ c.name }}</span><span class="text-muted-foreground">{{ modeLabel(c) }}</span>
        <button type="button" :aria-label="$t('编辑 /{name}', { name: c.name })" class="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-raised" @click="edit(i)"><Pencil class="size-3" /></button>
        <button type="button" :aria-label="$t('删除 /{name}', { name: c.name })" class="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-raised" @click="draft = draft.filter((_, j) => j !== i)"><X class="size-3" /></button>
      </span>
      <button type="button" class="inline-flex h-6 items-center gap-0.5 rounded-md border border-dashed border-input px-2 text-xs text-muted-foreground hover:bg-raised" @click="edit(null)"><Plus class="size-3" />{{ $t("添加命令") }}</button>
    </div>
    <div><el-button size="small" type="primary" :disabled="!dirty || save.isPending.value" @click="save.mutate()"><Spinner v-if="save.isPending.value" />{{ $t("保存") }}</el-button></div>
    <CommandDialog v-model:open="open" :command="current" :taken="taken" :prompt-only="false" :no-slash="''" @save="put" />
  </Card>
</template>
