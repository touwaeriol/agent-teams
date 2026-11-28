<script setup lang="ts">
import { $t } from "@/i18n";
/** The message box to the thread's lead: a done or failed thread reopens with the next message (quiet hint above), an archived one shows a note with 取消归档. */
import { computed } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { useSendMessage } from "@/api/pending";
import { keys, useMe } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { draftKey, useDraft } from "@/lib/persist";
import { leadOf } from "@/lib/present";
import { cn } from "@/lib/utils";
import { useAttachments } from "../attachments";
import Composer from "../Composer.vue";
import SessionControlsBar from "../session/controls/SessionControlsBar.vue";
import SessionFixed from "../session/SessionFixed.vue";
import { closedNote, reopenHint } from "./detail";

const props = defineProps<{ pid: string; thread: ThreadView; class?: string; session?: boolean }>();
const me = useMe();
// Drafts (text, attachments, @ references) are kept per viewer and thread across remounts.
const draft = computed(() => draftKey(me.data.value?.id ?? (me.isError.value ? "anon" : undefined), props.pid, props.thread.id));
const attachments = useAttachments(() => props.pid, undefined, draft);
const lead = computed(() => leadOf(props.thread));
const note = computed(() => closedNote(props.thread));
// While archived the box is hidden; its unsent text and attachments are kept for when it is back.
const unsent = useDraft(() => draft.value);
const keptDraft = computed(() => Boolean(unsent.value.trim()) || attachments.items.value.length > 0);
const hint = computed(() => {
  const h = reopenHint(props.thread);
  return h && props.session ? $t("发送消息会继续这个会话") : h;
});
const who = computed(() => (props.session ? $t("员工") : $t("组长")));
// The message shows in the chat at once (api/pending.ts); the box does not wait for the server.
const onSend = useSendMessage(() => props.thread.id, {
  invalidate: () => (hint.value ? [keys.threads(props.thread.project_id), keys.thread(props.thread.id)] : []),
  draft: () => draft.value,
  attachments,
  assist: () => ({ scope: "lead", pid: props.pid, cid: props.thread.id }),
});
const unarchive = useAction(() => threadActions.unarchive(props.thread.id), {
  invalidate: () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)],
  success: $t("#{no} 已取消归档", { no: props.thread.no }),
});
</script>

<template>
  <div :class="cn('shrink-0', props.class)">
    <div v-if="note" class="flex items-center justify-center gap-3 rounded-xl bg-background px-3 py-2.5 text-xs text-muted-foreground" data-testid="closed-note">
      <span>{{ note.text }}<template v-if="keptDraft">{{ $t("（输入框里未发送的内容会保留，取消归档后还在）") }}</template></span>
      <el-button v-if="note.unarchive" size="small" :loading="unarchive.isPending.value" @click="unarchive.mutate()">{{ $t("取消归档") }}</el-button>
    </div>
    <p v-if="!note && hint" class="px-1 pb-1 text-xs text-muted-foreground" data-testid="reopen-hint">{{ hint }}</p>
    <Composer
      v-if="!note"
      :id="`lead-${thread.id}`"
      :label="$t('发给{who}', { who })"
      :placeholder="session ? $t('回复员工；/ 命令和技能，{at} 引用文件', { at: '@' }) : $t('回复组长 {name}；/ 命令和技能，{at} 引用文件', { name: lead?.name ?? '', at: '@' })"
      :attachments="attachments"
      :on-send="onSend"
      :draft-key="draft"
      :assist="{ pid, cid: thread.id, scope: 'lead' }"
    >
      <template v-if="session" #controls><SessionFixed :thread="thread" /><SessionControlsBar :pid="pid" /></template>
    </Composer>
  </div>
</template>
