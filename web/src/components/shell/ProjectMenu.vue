<script setup lang="ts">
import { $t } from "@/i18n";
/** 置顶 / 重命名 / 归档 / 删除 (the "项目菜单" of the design). */
import { Archive, MoreHorizontal, Pencil, Pin, PinOff, Trash2 } from "lucide-vue-next";
import { ref } from "vue";
import { useRouter } from "vue-router";
import { projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ProjectView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";

const props = defineProps<{ project: ProjectView }>();
const router = useRouter();
const renaming = ref(false);
const name = ref(props.project.name);
const deleting = ref(false);
const invalidate = () => [keys.projects, keys.project(props.project.id)];
const update = useAction((body: { pinned?: boolean; archived?: boolean; name?: string }) => projectActions.update(props.project.id, body), { invalidate });
const remove = useAction(() => projectActions.remove(props.project.id), {
  invalidate: [keys.projects],
  success: $t("项目已删除"),
  onSuccess: () => void router.push("/projects"),
});

const COMMANDS: Record<string, () => void> = {
  pin: () => update.mutate({ pinned: !props.project.pinned }),
  rename: () => {
    name.value = props.project.name;
    renaming.value = true;
  },
  archive: () => update.mutate({ archived: !props.project.archived }),
  delete: () => (deleting.value = true),
};
const run = (c: string) => COMMANDS[c]?.();
const rename = () => update.mutate({ name: name.value.trim() }, { onSuccess: () => (renaming.value = false) });
</script>

<template>
  <DropdownMenu @command="run">
    <button
      type="button"
      :aria-label="$t('{name} 的菜单', { name: project.name })"
      class="mr-1 hidden size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background group-hover:flex aria-expanded:flex"
    >
      <MoreHorizontal class="size-4" />
    </button>
    <template #menu>
      <el-dropdown-item command="pin"><PinOff v-if="project.pinned" /><Pin v-else /> {{ project.pinned ? $t("取消置顶") : $t("置顶") }}</el-dropdown-item>
      <el-dropdown-item command="rename"><Pencil /> {{ $t("重命名") }}</el-dropdown-item>
      <el-dropdown-item command="archive"><Archive /> {{ project.archived ? $t("恢复") : $t("归档") }}</el-dropdown-item>
      <el-dropdown-item command="delete" divided class="is-destructive"><Trash2 /> {{ $t("删除") }}</el-dropdown-item>
    </template>
  </DropdownMenu>
  <FormDialog v-model:open="renaming" :title="$t('重命名项目')" :submit-label="$t('保存')" :busy="update.isPending.value" :disabled="!name.trim()" @submit="rename">
    <Field v-slot="{ id }" :label="$t('项目名称')">
      <el-input :id="id" v-model="name" autofocus />
    </Field>
  </FormDialog>
  <ConfirmAction
    v-model:open="deleting"
    :title="$t('删除项目「{name}」？', { name: project.name })"
    :description="$t('会关闭全部会话，连同讨论串记录一起删掉，不能恢复。')"
    :confirm-label="$t('删除')"
    @confirm="remove.mutate()"
  />
</template>
