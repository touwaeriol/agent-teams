<script setup lang="ts">
/**
 * A model field: the runtime's list (platform, then the user's own) plus any id typed in.
 * `emptyLabel` adds a choice for "" (follow the role / project setting).
 */
import { computed } from "vue";
import { useModels } from "@/api/queries";
import { toast } from "@/lib/toast";
import { MODEL_ID, modelChoices, type Runtime } from "@/lib/models";

const props = defineProps<{ runtime: Runtime; label: string; emptyLabel?: string; size?: "default" | "small" }>();
const value = defineModel<string>({ required: true });
const models = useModels();
const FOLLOW = "__follow__";
const choices = computed(() => modelChoices(models.data.value?.models ?? [], props.runtime, value.value));
const shown = computed(() => (value.value === "" && props.emptyLabel !== undefined ? FOLLOW : value.value));

function pick(v: string) {
  const next = v === FOLLOW ? "" : v.trim();
  if (next && !MODEL_ID.test(next)) return void toast.error("模型 ID 只能用字母、数字和 . _ - : / @ [ ]，最多 100 个字符");
  value.value = next;
}
</script>

<template>
  <el-select
    :model-value="shown"
    :aria-label="label"
    filterable
    allow-create
    default-first-option
    :size="size"
    placeholder="选择或输入模型 ID"
    @update:model-value="pick"
  >
    <el-option v-if="emptyLabel !== undefined" :value="FOLLOW" :label="emptyLabel" />
    <el-option v-for="c in choices" :key="c.value" :value="c.value" :label="c.label">
      <span class="flex items-center justify-between gap-3">
        <span>{{ c.label }}</span>
        <span class="text-xs text-muted-foreground">{{ c.isDefault ? "默认" : c.mine ? "我的" : c.custom ? "自定义" : c.label === c.value ? "" : c.value }}</span>
      </span>
    </el-option>
  </el-select>
</template>
