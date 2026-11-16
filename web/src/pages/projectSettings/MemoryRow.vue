<script setup lang="ts">
/** One memory: click to edit; on hover pin / unpin and delete in place of its source. */
import { Pin, PinOff, Trash2 } from "lucide-vue-next";
import { ref } from "vue";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { MemoryView } from "@/api/views";
import { memorySource } from "./logic";

const props = defineProps<{ pid: string; item: MemoryView }>();
const editing = ref(false);
const text = ref(props.item.text);
const invalidate = () => [keys.memory(props.pid)];
const save = useAction((body: { text?: string; pinned?: boolean }) => projects.updateMemory(props.pid, props.item.id, body), { invalidate, onSuccess: () => (editing.value = false) });
const remove = useAction(() => projects.removeMemory(props.pid, props.item.id), { invalidate });
function edit() {
  text.value = props.item.text;
  editing.value = true;
}
</script>

<template>
  <div v-if="editing" class="flex flex-col gap-2 rounded-xl bg-raised/60 px-3 py-2.5">
    <el-input v-model="text" type="textarea" :rows="2" autofocus aria-label="修改记忆" />
    <span class="flex gap-2">
      <el-button size="small" type="primary" :disabled="!text.trim() || save.isPending.value" @click="save.mutate({ text: text.trim() })">保存</el-button>
      <el-button size="small" class="!ml-0" @click="editing = false">取消</el-button>
    </span>
  </div>
  <div v-else class="group flex items-start gap-3 rounded-xl bg-raised/60 px-3 py-2.5">
    <button type="button" class="flex-1 text-left text-[13px] leading-[21px]" title="点一下修改" @click="edit">
      <Pin v-if="item.pinned" class="mr-1 inline size-3 text-[#C8622A]" />{{ item.text }}
    </button>
    <span class="whitespace-nowrap text-xs text-muted-foreground group-hover:hidden">{{ memorySource(item) }}</span>
    <span class="hidden gap-1 group-hover:flex">
      <button type="button" :aria-label="item.pinned ? '取消置顶' : '置顶'" class="text-muted-foreground hover:text-foreground" @click="save.mutate({ pinned: !item.pinned })">
        <PinOff v-if="item.pinned" class="size-3.5" /><Pin v-else class="size-3.5" />
      </button>
      <button type="button" aria-label="删除" class="text-muted-foreground hover:text-destructive" @click="remove.mutate()"><Trash2 class="size-3.5" /></button>
    </span>
  </div>
</template>
