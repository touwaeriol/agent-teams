<script setup lang="ts">
/** 重命名 / 删除 for one row or tile. */
import { MoreHorizontal } from "lucide-vue-next";
import { ref } from "vue";
import { deliverables as deliverableActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import DropdownMenu from "@/components/common/DropdownMenu.vue";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";

const props = defineProps<{ pid: string; item: DeliverableView }>();
const renaming = ref(false);
const title = ref(props.item.title);
const invalidate = () => [keys.allDeliverables(props.pid)];
const rename = useAction(() => deliverableActions.rename(props.item.id, title.value.trim()), { invalidate, onSuccess: () => (renaming.value = false) });
const remove = useAction(() => deliverableActions.remove(props.item.id), { invalidate, success: "已删除" });
function run(command: string) {
  if (command === "rename") {
    title.value = props.item.title;
    renaming.value = true;
  } else if (command === "delete") remove.mutate();
}
</script>

<template>
  <DropdownMenu placement="bottom-end" @command="run">
    <button
      type="button"
      :aria-label="`${item.title} 的菜单`"
      class="flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground opacity-0 hover:bg-background focus:opacity-100 group-hover:opacity-100 aria-expanded:opacity-100 max-md:opacity-100"
    >
      <MoreHorizontal class="size-4" />
    </button>
    <template #menu>
      <el-dropdown-item command="rename">重命名</el-dropdown-item>
      <el-dropdown-item command="delete" class="is-destructive">删除</el-dropdown-item>
    </template>
  </DropdownMenu>
  <FormDialog v-model:open="renaming" title="重命名" submit-label="保存" :busy="rename.isPending.value" :disabled="!title.trim()" @submit="rename.mutate()">
    <Field v-slot="{ id }" label="名称"><el-input :id="id" v-model="title" /></Field>
  </FormDialog>
</template>
