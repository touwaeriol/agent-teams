<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * Pick where the selected project-chat messages go: a thread (searchable, grouped like the
 * thread list; archived ones cannot be picked) or 新建讨论串 with a title (the messages become
 * its brief). Forwards on submit, then opens the thread.
 */
import { MessageSquarePlus, Search } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ForwardView, MessageView, ThreadView } from "@/api/views";
import FormDialog from "@/components/common/FormDialog.vue";
import { pickable, pickerGroups, titleFrom } from "@/lib/forward";
import { cn } from "@/lib/utils";
import ThreadMark from "../ThreadMark.vue";

const props = defineProps<{ cid: string; ids: number[]; messages: MessageView[]; threads: ThreadView[] }>();
const open = defineModel<boolean>("open", { required: true });
const emit = defineEmits<{ done: [v: ForwardView] }>();

type Target = { kind: "thread"; no: number } | { kind: "new" } | null;
const q = ref("");
const target = ref<Target>(null);
const title = ref("");
const groups = computed(() => pickerGroups(props.threads, q.value));
const chosen = computed(() => props.messages.filter((m) => props.ids.includes(Number(m.id))));
watch(open, (v) => {
  if (!v) return;
  q.value = "";
  target.value = null;
  title.value = titleFrom(chosen.value);
});

const send = useAction(
  () => {
    const t = target.value!;
    const body = t.kind === "thread" ? { message_ids: props.ids, thread_no: t.no } : { message_ids: props.ids, new_thread: { title: title.value.trim() } };
    return conversations.forward(props.cid, body);
  },
  {
    invalidate: () => [keys.messages(props.cid)],
    success: (v: ForwardView) => (v.duplicate ? $t("刚刚已经转发到 #{thread_no} 了", { thread_no: v.thread_no }) : v.created ? $t("已新开讨论串 #{thread_no}", { thread_no: v.thread_no }) : $t("已转发 {count} 条消息到 #{thread_no}", { count: v.count, thread_no: v.thread_no })),
    onSuccess: (v: ForwardView) => {
      open.value = false;
      emit("done", v);
    },
  },
);
const isThread = (no: number) => target.value?.kind === "thread" && target.value.no === no;
const ready = computed(() => target.value !== null && (target.value.kind === "thread" || title.value.trim() !== ""));
</script>

<template>
  <FormDialog v-model:open="open" :title="$t('转发到讨论串')" :description="$t('把你选的 {count} 条消息原样发给组长（图片、附件和 {at} 引用一起带上）', { count: ids.length, at: '@' })" :submit-label="$t('转发')" :busy="send.isPending.value" :disabled="!ready" @submit="send.mutate()">
    <label class="flex h-9 items-center gap-2 rounded-lg border border-input bg-panel px-3 text-sm">
      <Search class="size-4 text-muted-foreground" />
      <input v-model="q" type="search" :aria-label="$t('搜索讨论串')" :placeholder="$t('搜索讨论串')" class="min-w-0 flex-1 bg-transparent outline-none" />
    </label>
    <div class="-mx-1 flex max-h-[min(52vh,420px)] flex-col gap-3 overflow-y-auto px-1" role="listbox" :aria-label="$t('讨论串')">
      <div>
        <button
          type="button"
          role="option"
          :aria-selected="target?.kind === 'new'"
          :class="cn('flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm hover:bg-raised', target?.kind === 'new' && 'bg-raised font-medium')"
          @click="target = { kind: 'new' }"
        >
          <MessageSquarePlus class="size-4 text-muted-foreground" />
          {{ $t("新建讨论串") }}
        </button>
        <input
          v-if="target?.kind === 'new'"
          v-model="title"
          :aria-label="$t('新讨论串的标题')"
          maxlength="200"
          :placeholder="$t('讨论串标题')"
          class="mt-1.5 h-9 w-full rounded-lg border border-input bg-panel px-3 text-sm outline-none focus:border-primary"
        />
      </div>
      <section v-for="g in groups" :key="g.id" class="flex flex-col">
        <h3 class="px-2.5 pb-1 text-xs text-muted-foreground">{{ g.label }}</h3>
        <button
          v-for="t in g.items"
          :key="t.id"
          type="button"
          role="option"
          :aria-selected="isThread(t.no)"
          :disabled="!pickable(t)"
          :title="pickable(t) ? undefined : $t('已归档的讨论串要先取消归档')"
          :class="cn('flex w-full items-start gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm hover:bg-raised disabled:cursor-not-allowed disabled:opacity-50', isThread(t.no) && 'bg-raised font-medium')"
          @click="target = { kind: 'thread', no: t.no }"
        >
          <ThreadMark :thread="t" />
          <span class="min-w-0 flex-1 truncate"><span class="text-muted-foreground">#{{ t.no }}</span> {{ t.title }}</span>
        </button>
      </section>
      <p v-if="groups.length === 0 && q" class="py-6 text-center text-[13px] text-muted-foreground">{{ $t("没有匹配的讨论串") }}</p>
    </div>
  </FormDialog>
</template>
