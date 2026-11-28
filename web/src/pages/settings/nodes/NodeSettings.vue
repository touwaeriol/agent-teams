<script setup lang="ts">
import { $t } from "@/i18n";
/** A node's settings: slots, park timeout, labels and whether the machine is isolated. */
import { ref, watch } from "vue";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { NodeView } from "@/api/views";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import TagInput from "@/components/common/TagInput.vue";
import { settingsPatch } from "./nodes";

const props = defineProps<{ node: NodeView }>();
const open = defineModel<boolean>("open", { required: true });
const max = ref("");
const park = ref("");
const isolated = ref(false);
const labels = ref<string[]>([]);
function reset() {
  max.value = props.node.max_active_sessions?.toString() ?? "";
  park.value = props.node.park_after_s?.toString() ?? "";
  isolated.value = props.node.isolated;
  labels.value = [...(props.node.labels ?? [])];
}
watch(open, (o) => o && reset(), { immediate: true });
const save = useAction(() => nodeActions.update(props.node.id, settingsPatch(max.value, park.value, isolated.value, labels.value)), {
  invalidate: [keys.nodes],
  success: $t("节点设置已下发"),
  onSuccess: () => (open.value = false),
});
</script>

<template>
  <FormDialog v-model:open="open" :title="$t('{name} 的设置', { name: node.name })" :submit-label="$t('保存')" :busy="save.isPending.value" @submit="save.mutate()">
    <div class="grid gap-3 sm:grid-cols-2">
      <Field v-slot="{ id }" :label="$t('最多同时运行的会话')" :hint="$t('留空不限（守护进程用 --max-sessions 设了上限时按它）')"><el-input :id="id" v-model="max" type="number" :min="1" /></Field>
      <Field v-slot="{ id }" :label="$t('空闲多久后停放（秒）')" :hint="$t('留空用默认 900')"><el-input :id="id" v-model="park" type="number" :min="60" /></Field>
    </div>
    <Field v-slot="{ id }" :label="$t('标签')"><TagInput :id="id" v-model="labels" placeholder="gpu、big-disk" /></Field>
    <label class="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 text-[13px]">
      <span class="flex flex-col gap-0.5">
        <span class="font-medium">{{ $t("这台机器是隔离的") }}</span>
        <span class="text-xs text-muted-foreground">{{ $t("整台机器只给员工用、没有其他秘密时打开；Codex 和 Antigravity 岗位只派到隔离的节点。") }}</span>
      </span>
      <el-switch v-model="isolated" />
    </label>
  </FormDialog>
</template>
