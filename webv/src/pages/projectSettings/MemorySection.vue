<script setup lang="ts">
// 记忆: what the coordinator, leads and members carry into every session; add, edit, pin, delete.
import { ref } from "vue";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useMemory } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import Heading from "./Heading.vue";
import MemoryRow from "./MemoryRow.vue";

const props = defineProps<{ pid: string }>();
const memory = useMemory(() => props.pid);
const adding = ref(false);
const text = ref("");
const add = useAction(() => projects.addMemory(props.pid, text.value.trim()), {
  invalidate: () => [keys.memory(props.pid)],
  onSuccess: () => {
    text.value = "";
    adding.value = false;
  },
});
const submit = () => text.value.trim() && add.mutate();
</script>

<template>
  <div class="flex flex-col gap-3">
    <Heading>
      记忆
      <template #action><el-button size="small" @click="adding = true">添加一条</el-button></template>
    </Heading>
    <p class="-mt-2 text-[13px] text-muted-foreground">讨论串结束时由组长总结、统筹整理。统筹、组长和成员开工时都会带上。你可以改、可以删。</p>
    <form v-if="adding" class="flex flex-col gap-2" @submit.prevent="submit">
      <el-input v-model="text" type="textarea" :rows="2" autofocus placeholder="例如：改完前端要跑 tsc 和 vite build" aria-label="新的记忆" />
      <span class="flex gap-2">
        <el-button size="small" type="primary" native-type="submit" :disabled="!text.trim() || add.isPending.value">添加</el-button>
        <el-button size="small" class="!ml-0" @click="adding = false">取消</el-button>
      </span>
    </form>
    <Loading v-if="memory.isLoading.value" />
    <p v-if="memory.data.value?.length === 0 && !adding" class="text-[13px] text-muted-foreground">还没有记忆。</p>
    <div class="flex flex-col gap-1.5">
      <MemoryRow v-for="m in memory.data.value ?? []" :key="m.id" :pid="pid" :item="m" />
    </div>
  </div>
</template>
