<script setup lang="ts">
/** "存为共享文件": the target path (default the file name) and, after a 409 file_exists, an overwrite choice. */
import { ref, watch } from "vue";
import { ApiError } from "@/api/client";
import FormDialog from "@/components/common/FormDialog.vue";
import { markExpired, saveAttachmentShared } from "@/lib/attachmentActions";
import { sharedPathProblem } from "@/lib/sharedFiles";
import { toast } from "@/lib/toast";

const props = defineProps<{ attachment: { id: string; name: string } | null }>();
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ saved: [path: string]; expired: [id: string] }>();
const path = ref("");
const overwrite = ref(false);
const exists = ref(false);
const error = ref("");
const busy = ref(false);
watch(open, (o) => {
  if (!o) return;
  path.value = props.attachment?.name.replace(/^\.+/, "") ?? "";
  overwrite.value = false;
  exists.value = false;
  error.value = "";
});
async function submit() {
  const a = props.attachment;
  if (!a || busy.value) return;
  const bad = sharedPathProblem(path.value);
  if (bad) {
    error.value = bad;
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    const f = await saveAttachmentShared(a.id, path.value, overwrite.value);
    toast.success(f.overwritten ? `已存为共享文件 ${f.path}（覆盖了原来的文件）` : `已存为共享文件 ${f.path}`);
    emit("saved", f.path);
    open.value = false;
  } catch (e) {
    if (e instanceof ApiError && e.code === "file_exists") {
      exists.value = true;
      error.value = `共享文件里已经有 ${path.value}。勾选“覆盖”后再保存，或者换一个路径。`;
    } else if (e instanceof ApiError && e.status === 410) {
      markExpired(a.id);
      emit("expired", a.id);
      error.value = e.message || "附件已过期，不能再保存";
    } else {
      error.value = e instanceof Error ? e.message : String(e);
    }
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <FormDialog
    v-model:open="open"
    title="存为共享文件"
    description="存进项目的共享文件后会一直保留（附件本身最后一次使用 24 小时后会过期），项目里的统筹、组长和成员都能用工具读到。"
    submit-label="保存"
    :busy="busy"
    :disabled="!path.trim()"
    width="28rem"
    @submit="submit"
  >
    <label class="flex flex-col gap-1.5 text-sm">
      <span class="font-medium">路径</span>
      <el-input v-model="path" placeholder="例如 inputs/需求.pdf" aria-label="共享文件路径" />
    </label>
    <el-checkbox v-if="exists" v-model="overwrite">覆盖已有的同名文件</el-checkbox>
    <p v-if="error" class="text-[13px] text-destructive" role="alert">{{ error }}</p>
  </FormDialog>
</template>
