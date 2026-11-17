<script setup lang="ts">
/** 我的模型: model ids this user adds to the platform list (for /model and the role model field). */
import { computed } from "vue";
import { models as modelActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useModels } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import ModelListEditor from "@/components/app/models/ModelListEditor.vue";
import { editable, modelName, runtimeLabel } from "@/lib/models";

const models = useModels();
const all = computed(() => models.data.value?.models ?? []);
const platform = computed(() => all.value.filter((m) => !m.mine));
const mine = computed(() => editable(all.value, true));
const save = useAction(modelActions.saveMine, { invalidate: [keys.models], success: "模型列表已保存" });
</script>

<template>
  <Card class="gap-3">
    <div class="flex flex-col gap-1">
      <span class="font-semibold">模型</span>
      <span class="text-xs leading-5 text-muted-foreground">
        /model 和岗位的模型字段从这里选。平台提供：{{ platform.map((m) => `${modelName(all, m.model)}（${runtimeLabel(m.runtime)}${m.default ? "，默认" : ""}）`).join("、") || "无" }}。下面加的模型只有你能看到。
      </span>
    </div>
    <ModelListEditor :rows="mine" :busy="save.isPending.value" @save="(l) => save.mutate(l)" />
  </Card>
</template>
