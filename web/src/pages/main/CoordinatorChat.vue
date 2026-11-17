<script setup lang="ts">
/**
 * The coordinator conversation, with the questions it asked drawn where they were asked. The
 * user's bubbles can be selected and forwarded to a thread (lib/forward), and ?msg=<id> scrolls
 * to that message (the link under a forwarded copy in a thread chat).
 */
import { computed, nextTick, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useMessages, useQuestions } from "@/api/queries";
import type { CoordinatorView, QuestionView, ThreadView } from "@/api/views";
import Chat from "@/components/app/Chat.vue";
import WaitingCard from "@/components/app/cards/WaitingCard.vue";
import Loading from "@/components/common/Loading.vue";
import { chatQuestions } from "@/lib/ask";
import { isSelecting, messageAnchor, selection, startSelecting, toggle } from "@/lib/forward";

const props = defineProps<{ pid: string; coordinator?: CoordinatorView; threads: ThreadView[] }>();
const emit = defineEmits<{ openThread: [no: number] }>();
const cid = computed(() => props.coordinator?.conversation_id);
const questions = useQuestions(cid);
const inserts = computed(() => (cid.value ? chatQuestions(questions.data.value).map((q: QuestionView) => ({ key: q.id, at: q.created_at, question: q })) : []));
const selecting = computed(() => isSelecting(cid.value));

// ?msg=<id>: load older pages until the message is there, scroll to it, flash it, drop the query.
const route = useRoute();
const router = useRouter();
const messages = useMessages(cid);
async function jump(id: string) {
  for (let i = 0; i < 20; i += 1) {
    await nextTick();
    const el = document.getElementById(messageAnchor(id));
    if (el) {
      el.scrollIntoView({ block: "center", behavior: "smooth" });
      el.classList.add("at-flash");
      setTimeout(() => el.classList.remove("at-flash"), 2000);
      break;
    }
    if (!messages.hasNextPage.value) break;
    await messages.fetchNextPage();
  }
  const query = { ...route.query };
  delete query.msg;
  void router.replace({ query });
}
watch(
  () => [route.query.msg, cid.value, messages.isLoading.value] as const,
  ([msg, c, loading]) => typeof msg === "string" && c && !loading && void jump(msg),
  { immediate: true },
);
</script>

<template>
  <Loading v-if="!coordinator" class="py-16" />
  <Chat
    v-else
    :cid="cid"
    :pid="pid"
    :threads="threads"
    :inserts="inserts"
    speaker-color="#C8622A"
    forwardable
    :selecting="selecting"
    :selected-ids="selection.ids"
    @open-thread="(no) => emit('openThread', no)"
    @forward="(id) => startSelecting(cid!, id)"
    @toggle="toggle"
  >
    <template #insert="{ item }"><WaitingCard :cid="cid!" :pid="pid" :question="item.question" variant="soft" /></template>
    <template #empty>
      <div class="flex flex-col items-center gap-2 py-16 text-center">
        <p class="text-[15px] font-medium">和项目统筹说说要做什么</p>
        <p class="max-w-md text-[13px] leading-6 text-muted-foreground">统筹只分派、不改代码：它会判断要做什么，开讨论串让组长带成员在仓库里完成，有结论回到这里告诉你。</p>
      </div>
    </template>
  </Chat>
</template>
