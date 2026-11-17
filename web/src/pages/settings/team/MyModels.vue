<script setup lang="ts">
/** 模型: the user's own model list (for /model and the role model field), its order and per-type defaults. */
import { computed } from "vue";
import { models as modelActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useModels } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import ModelListEditor from "@/components/app/models/ModelListEditor.vue";

const models = useModels();
const rows = computed(() => (models.data.value?.models ?? []).map((m) => ({ ...m })));
const save = useAction(modelActions.saveMine, { invalidate: [keys.models], success: "模型列表已保存" });
</script>

<template>
  <Card id="my-models" class="scroll-mt-4 gap-3">
    <div class="flex flex-col gap-1">
      <span class="font-semibold">模型</span>
      <span class="text-xs leading-5 text-muted-foreground">
        /model 和岗位模型字段按这个顺序列出这里的模型。每种类型可以设一个“我的默认”：岗位和项目都没指定模型时，会话用它。
      </span>
    </div>
    <ModelListEditor :rows="rows" :busy="save.isPending.value" @save="(l) => save.mutate(l)" />
  </Card>
</template>
