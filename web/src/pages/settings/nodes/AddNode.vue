<script setup lang="ts">
/** 添加节点: a name, then the one-time install command (shown by the page). */
import { ref } from "vue";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { Enrollment } from "@/api/views";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";

const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ created: [name: string, enrollment: Enrollment] }>();
const name = ref("");
const create = useAction(() => nodeActions.create(name.value.trim()), {
  invalidate: [keys.nodes],
  onSuccess: (r) => {
    open.value = false;
    name.value = "";
    emit("created", r.node.name, { token: r.token, install: r.install, docker: r.docker, compose: r.compose });
  },
});
</script>

<template>
  <FormDialog v-model:open="open" title="添加节点" description="起个名字，下一步给你启动命令：宿主机安装、docker run 或 compose.yml。" submit-label="创建" :busy="create.isPending.value" :disabled="!name.trim()" @submit="create.mutate()">
    <Field v-slot="{ id }" label="节点名称" hint="字母、数字、- 和 _，例如 rc-erio">
      <el-input :id="id" v-model="name" class="at-mono" />
    </Field>
  </FormDialog>
</template>
