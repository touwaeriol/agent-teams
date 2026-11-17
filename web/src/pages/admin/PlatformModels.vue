<script setup lang="ts">
/** 模型列表: the platform's models per type, in order; the default is what sessions open with when nothing else names a model. */
import { computed } from "vue";
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useModels } from "@/api/queries";
import Card from "@/components/app/Card.vue";
import ModelListEditor from "@/components/app/models/ModelListEditor.vue";
import { editable } from "@/lib/models";

const models = useModels();
const rows = computed(() => editable(models.data.value?.models ?? [], false));
const save = useAction(admin.saveModels, { invalidate: [keys.models], success: "平台模型列表已保存" });
</script>

<template>
  <Card class="gap-3">
    <div class="flex flex-col gap-1">
      <span class="font-semibold">模型列表</span>
      <span class="text-xs leading-5 text-muted-foreground">所有用户的 /model 和岗位模型字段按这个顺序列出；每种类型的“默认”是岗位和项目都没指定模型时会话用的模型。用户还可以加自己的模型。</span>
    </div>
    <ModelListEditor :rows="rows" with-default :busy="save.isPending.value" @save="(l) => save.mutate(l)" />
  </Card>
</template>
