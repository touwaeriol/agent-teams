<script setup lang="ts">
/** A memory file of a project (pid) or my own (pid null): read it (Markdown), edit name /
 * description / content, delete a topic file. `name` "" starts a new file in edit mode. MEMORY.md
 * has no description and cannot be deleted; a topic file keeps its type. */
import { Pencil, Trash2 } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { useAction } from "@/api/mutations";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Field from "@/components/common/Field.vue";
import Loading from "@/components/common/Loading.vue";
import Markdown from "@/components/Markdown.vue";
import { timeAgo } from "@/lib/utils";
import { MEMORY_INDEX, type MemoryBody, memoryFileName, memorySource, memoryWriter } from "./memory";

const props = defineProps<{ pid: string | null; name: string }>();
const emit = defineEmits<{ close: [] }>();
const src = memorySource(props.pid);
const isNew = computed(() => props.name === "");
const isIndex = computed(() => props.name === MEMORY_INDEX);
const file = src.useFile(() => props.name);
const editing = ref(isNew.value);
const draft = ref({ name: "", description: "", content: "" });
function reset() {
  const f = file.data.value;
  draft.value = { name: props.name, description: f?.description ?? "", content: f?.content ?? "" };
}
watch(() => file.data.value, reset, { immediate: true });

const invalidate = src.invalidate;
const target = computed(() => (isNew.value ? memoryFileName(draft.value.name) : props.name));
// The type is kept as it is (the page does not edit it).
const body = (): MemoryBody => ({ description: draft.value.description, content: draft.value.content, type: (file.data.value?.type ?? "") as MemoryBody["type"] });
const save = useAction(() => src.put(target.value, body()), {
  invalidate,
  success: "已保存，新开的会话会读到",
  onSuccess: () => emit("close"),
});
const remove = useAction(() => src.remove(props.name), { invalidate, success: "已删除", onSuccess: () => emit("close") });
const canSave = computed(() => Boolean(target.value) && (isIndex.value || draft.value.description.trim() !== "") && !save.isPending.value);
const close = (o: boolean) => !o && emit("close");
const title = computed(() => (isNew.value ? "新建记忆文件" : props.name));
</script>

<template>
  <el-dialog :model-value="true" align-center append-to-body class="at-dialog w-[calc(100%-2rem)] max-w-[720px]" width="720px" :aria-label="title" @update:model-value="close">
    <template #header>
      <div class="flex flex-col gap-1 pr-6">
        <h2 class="font-mono text-[15px] font-medium">{{ title }}</h2>
        <p v-if="file.data.value" class="text-[13px] text-muted-foreground">
          {{ memoryWriter(file.data.value.updated_by) }} · 更新于 {{ timeAgo(file.data.value.updated_at) }}
        </p>
        <p v-else-if="isIndex" class="text-[13px] text-muted-foreground">还没有内容。每个新会话开始时都会读到这里。</p>
      </div>
    </template>
    <Loading v-if="!isNew && file.isLoading.value" />
    <form v-else-if="editing" class="flex flex-col gap-3" @submit.prevent="canSave && save.mutate()">
      <Field v-if="isNew" v-slot="{ id }" label="文件名" hint="例如 testing.md；不写 .md 会自动补上。">
        <el-input :id="id" v-model="draft.name" placeholder="testing.md" class="font-mono" />
      </Field>
      <Field v-if="!isIndex" v-slot="{ id }" label="描述" hint="一句话，会列在 MEMORY.md 里，帮员工判断要不要读这个文件。">
        <el-input :id="id" v-model="draft.description" maxlength="300" placeholder="怎么跑测试、常见失败的处理" />
      </Field>
      <Field v-slot="{ id }" label="内容（Markdown）">
        <el-input :id="id" v-model="draft.content" type="textarea" :rows="14" maxlength="32000" show-word-limit class="font-mono" />
      </Field>
      <div class="flex justify-end gap-2">
        <el-button @click="isNew ? emit('close') : ((editing = false), reset())">取消</el-button>
        <el-button type="primary" native-type="submit" :disabled="!canSave">保存</el-button>
      </div>
    </form>
    <div v-else class="flex flex-col gap-4">
      <p v-if="file.data.value?.description" class="text-[13px] text-muted-foreground">{{ file.data.value.description }}</p>
      <div class="scroll-thin max-h-[55vh] overflow-y-auto rounded-lg border bg-raised/40 px-4 py-3">
        <Markdown v-if="draft.content.trim()" :source="draft.content" />
        <p v-else class="text-[13px] text-muted-foreground">（空）</p>
      </div>
      <div class="flex justify-end gap-2">
        <ConfirmAction v-if="!isIndex" :title="`删除 ${name}？`" description="同时从 MEMORY.md 里去掉它的那一行，不能恢复。" confirm-label="删除" @confirm="remove.mutate()">
          <Trash2 /> 删除
        </ConfirmAction>
        <el-button type="primary" @click="editing = true"><Pencil />编辑</el-button>
      </div>
    </div>
  </el-dialog>
</template>
