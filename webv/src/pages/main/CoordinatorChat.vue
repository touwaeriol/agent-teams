<script setup lang="ts">
/** The coordinator conversation, with the questions it asked drawn where they were asked. */
import { computed } from "vue";
import { useQuestions } from "@/api/queries";
import type { CoordinatorView, QuestionView, ThreadView } from "@/api/views";
import Chat from "@/components/app/Chat.vue";
import WaitingCard from "@/components/app/cards/WaitingCard.vue";
import Loading from "@/components/common/Loading.vue";
import { chatQuestions } from "@/lib/ask";

const props = defineProps<{ pid: string; coordinator?: CoordinatorView; threads: ThreadView[] }>();
const emit = defineEmits<{ openThread: [no: number] }>();
const cid = computed(() => props.coordinator?.conversation_id);
const questions = useQuestions(cid);
const inserts = computed(() => (cid.value ? chatQuestions(questions.data.value).map((q: QuestionView) => ({ key: q.id, at: q.created_at, question: q })) : []));
</script>

<template>
  <Loading v-if="!coordinator" class="py-16" />
  <Chat v-else :cid="cid" :pid="pid" :threads="threads" :inserts="inserts" speaker-color="#C8622A" @open-thread="(no) => emit('openThread', no)">
    <template #insert="{ item }"><WaitingCard :cid="cid!" :pid="pid" :question="item.question" variant="soft" /></template>
    <template #empty>
      <div class="flex flex-col items-center gap-2 py-16 text-center">
        <p class="text-[15px] font-medium">和项目统筹说说要做什么</p>
        <p class="max-w-md text-[13px] leading-6 text-muted-foreground">统筹只分派、不改代码：它会判断要做什么，开讨论串让组长带成员在仓库里完成，有结论回到这里告诉你。</p>
      </div>
    </template>
  </Chat>
</template>
