<script setup lang="ts">
/** A role's model: "" follows the user's default of the runtime (named in the choice), else one of the runtime's models. */
import { computed } from "vue";
import { useModels } from "@/api/queries";
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import { defaultModel, followLabel, modelName, type Runtime } from "@/lib/models";

const props = defineProps<{ runtime: Runtime }>();
const value = defineModel<string>({ required: true });
const models = useModels();
const list = computed(() => models.data.value?.models ?? []);
/** The closed field's text: shorter than the choice so it fits a third of a card. */
const short = computed(() => {
  const id = defaultModel(list.value, props.runtime);
  return `默认（${id ? modelName(list.value, id) : "未设置"}）`;
});
</script>

<template>
  <ModelPicker v-model="value" :runtime="runtime" label="模型" :empty-label="followLabel(list, runtime)" :empty-short="short" class="at-select-36 at-text-13" />
</template>
