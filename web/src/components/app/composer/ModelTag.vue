<script setup lang="ts">
/**
 * The model the box's employee (project coordinator or thread lead) runs with, read-only: they
 * always use the owner's default, so there is nothing to switch here. Hidden until both the
 * conversation's model and the model list are loaded (no placeholder, no id-then-label flicker).
 */
import { computed } from "vue";
import { useConversationModel, useModels } from "@/api/queries";
import { modelName } from "@/lib/models";

const props = defineProps<{ cid: string }>();
const state = useConversationModel(() => props.cid);
const models = useModels();
const name = computed(() => {
  const id = state.data.value?.model;
  if (!id || models.isLoading.value) return "";
  return modelName(models.data.value?.models ?? [], id);
});
const tip = computed(() => `当前模型：${name.value}，统筹和组长使用默认模型，不能切换`);
</script>

<template>
  <span
    v-if="name"
    data-testid="composer-model"
    :aria-label="tip"
    :title="tip"
    class="max-w-[10rem] shrink-0 truncate whitespace-nowrap rounded-full bg-raised px-2 py-0.5 text-[11px] leading-4 text-muted-foreground"
  >{{ name }}</span>
</template>
