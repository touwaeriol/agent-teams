<script setup lang="ts">
/**
 * A model field that only offers the user's own models of one runtime (no typed ids; the server
 * rejects others). "" is the first choice (`emptyLabel`, shown as `emptyShort` when closed). An old
 * value missing from the list stays shown, marked 不在列表. The footer links to 我的模型.
 */
import { computed, nextTick, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useModels } from "@/api/queries";
import { modelChoices, type Runtime } from "@/lib/models";

const props = defineProps<{ runtime: Runtime; label: string; emptyLabel: string; emptyShort?: string; size?: "default" | "small" }>();
const value = defineModel<string>({ required: true });
const models = useModels();
const route = useRoute();
const router = useRouter();
const select = ref<{ blur: () => void } | null>(null);
const FOLLOW = "__follow__";
const choices = computed(() => modelChoices(models.data.value?.models ?? [], props.runtime, value.value));
const shown = computed(() => {
  if (!value.value) return props.emptyShort ?? props.emptyLabel;
  const c = choices.value.find((x) => x.value === value.value);
  return c?.custom ? `${c.label}（不在列表）` : (c?.label ?? value.value);
});

/** On 团队岗位 scroll down to the list; elsewhere open it in a new tab so a form in progress stays. */
async function manage() {
  select.value?.blur();
  const target = { path: "/settings/team", hash: "#my-models" };
  if (route.path !== target.path) return void window.open(router.resolve(target).href, "_blank", "noopener");
  await router.push(target);
  await nextTick();
  document.getElementById("my-models")?.scrollIntoView({ behavior: "smooth", block: "start" });
}
</script>

<template>
  <el-select ref="select" :model-value="value || FOLLOW" :aria-label="label" :size="size" popper-class="at-model-picker" @update:model-value="(v: string) => (value = v === FOLLOW ? '' : v)">
    <template #label><span :title="value ? shown : emptyLabel">{{ shown }}</span></template>
    <el-option :value="FOLLOW" :label="emptyLabel" />
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
