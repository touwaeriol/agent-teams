<script setup lang="ts">
/**
 * A role's model: "" (follow the user's default of the runtime) or one of the runtime's models in
 * the user's list. No typed ids; an old value missing from the list stays shown, marked 不在列表.
 * The footer links to 我的模型 further down the page.
 */
import { computed, nextTick, ref } from "vue";
import { useRouter } from "vue-router";
import { useModels } from "@/api/queries";
import { defaultModel, followLabel, modelChoices, modelName, type Runtime } from "@/lib/models";

const props = defineProps<{ runtime: Runtime; modelValue: string }>();
const emit = defineEmits<{ "update:modelValue": [model: string] }>();
const models = useModels();
const router = useRouter();
const select = ref<{ blur: () => void } | null>(null);
const FOLLOW = "__follow__";
const list = computed(() => models.data.value?.models ?? []);
const choices = computed(() => modelChoices(list.value, props.runtime, props.modelValue));
const follow = computed(() => followLabel(list.value, props.runtime));
/** The closed field's text: shorter than the option so it fits a third of a card. */
const shown = computed(() => {
  if (!props.modelValue) {
    const id = defaultModel(list.value, props.runtime);
    return `默认（${id ? modelName(list.value, id) : "未设置"}）`;
  }
  const c = choices.value.find((x) => x.value === props.modelValue);
  return c?.custom ? `${c.label}（不在列表）` : (c?.label ?? props.modelValue);
});

async function manage() {
  select.value?.blur();
  await router.push({ path: "/settings/team", hash: "#my-models" });
  await nextTick();
  document.getElementById("my-models")?.scrollIntoView({ behavior: "smooth", block: "start" });
}
</script>

<template>
  <el-select
    ref="select"
    :model-value="modelValue || FOLLOW"
    aria-label="模型"
    class="at-select-36 at-text-13"
    popper-class="at-role-model"
    @update:model-value="(v: string) => emit('update:modelValue', v === FOLLOW ? '' : v)"
  >
    <template #label><span :title="modelValue ? shown : follow">{{ shown }}</span></template>
    <el-option :value="FOLLOW" :label="follow" />
    <el-option v-for="c in choices" :key="c.value" :value="c.value" :label="c.custom ? `${c.label}（不在列表）` : c.label">
      <span class="flex items-center justify-between gap-3">
        <span class="truncate">{{ c.label }}</span>
        <span class="shrink-0 text-xs" :class="c.custom ? 'text-[#B4542F]' : 'text-muted-foreground'">{{ c.custom ? "不在列表" : c.isDefault ? "默认" : "" }}</span>
      </span>
    </el-option>
    <template #footer>
      <button type="button" class="w-full text-left text-[13px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline" @click="manage">管理我的模型</button>
    </template>
  </el-select>
</template>
