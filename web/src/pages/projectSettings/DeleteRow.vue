<script setup lang="ts">
import { Trash2 } from "lucide-vue-next";
import { useRouter } from "vue-router";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Row from "./Row.vue";

const props = defineProps<{ project: ProjectView }>();
const emit = defineEmits<{ close: [] }>();
const router = useRouter();
const remove = useAction(() => projects.remove(props.project.id), {
  invalidate: [keys.projects],
  success: "项目已删除",
  onSuccess: () => {
    emit("close");
    void router.push("/projects");
  },
});
</script>

<template>
  <Row title="删除项目" desc="删除此项目及其所有讨论串、交付物和记忆。此操作无法撤销。">
    <ConfirmAction :title="`删除项目「${project.name}」？`" description="会关闭全部会话，连同讨论串记录一起删掉，不能恢复。" confirm-label="删除" size="small" @confirm="remove.mutate()">
      <Trash2 /> 删除
    </ConfirmAction>
  </Row>
</template>
