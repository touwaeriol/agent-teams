<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * The model the box's employee (project coordinator or thread lead) runs with, read-only: they
 * have no /model; their model is set in the role or the project settings (the server works out
 * project → role → built-in default). Hidden until the conversation's model is loaded.
 */
import { computed } from "vue";
import { useConversationModel } from "@/api/queries";
import { modelName } from "@/lib/models";

const props = defineProps<{ cid: string }>();
const state = useConversationModel(() => props.cid);
const id = computed(() => state.data.value?.model ?? "");
const name = computed(() => (id.value ? modelName(id.value) : ""));
const tip = computed(() => $t("当前模型：{id}。统筹和组长不能用 /model 切换，请在岗位配置或项目设置里改", { id: id.value }));
</script>

<template>
  <span
    v-if="name"
    data-testid="composer-model"
    :aria-label="tip"
    :title="tip"
    class="shrink-0 whitespace-nowrap rounded-full bg-raised px-2 py-0.5 text-[11px] leading-4 text-muted-foreground"
  >{{ name }}</span>
</template>
