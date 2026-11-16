<script setup lang="ts">
/** The message box to the thread's lead; a closed note once the thread has finished (archived: with 取消归档). */
import { computed } from "vue";
import { conversations, threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { leadOf } from "@/lib/present";
import { cn } from "@/lib/utils";
import { useAttachments } from "../attachments";
import Composer from "../Composer.vue";
import { closedNote } from "./detail";

const props = defineProps<{ pid: string; thread: ThreadView; class?: string }>();
const attachments = useAttachments(() => props.pid);
const lead = computed(() => leadOf(props.thread));
const note = computed(() => closedNote(props.thread));
const send = useAction((v: { text: string; images: string[] }) => conversations.send(props.thread.id, v.text, false, v.images), {
  invalidate: () => [keys.messages(props.thread.id)],
});
const unarchive = useAction(() => threadActions.unarchive(props.thread.id), {
  invalidate: () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)],
  success: `#${props.thread.no} 已取消归档`,
});
const onSend = (text: string, images: string[]) => send.mutateAsync({ text, images });
</script>

<template>
  <div :class="cn('shrink-0', props.class)">
    <div v-if="note" class="flex items-center justify-center gap-3 rounded-xl bg-background px-3 py-2.5 text-xs text-muted-foreground" data-testid="closed-note">
      <span>{{ note.text }}</span>
      <el-button v-if="note.unarchive" size="small" :loading="unarchive.isPending.value" @click="unarchive.mutate()">取消归档</el-button>
    </div>
    <Composer
      v-else
      :id="`lead-${thread.id}`"
      label="发给组长"
      :placeholder="`回复组长 ${lead?.name ?? ''}，可粘贴或拖入图片`"
      :busy="send.isPending.value"
      :attachments="attachments"
      :on-send="onSend"
      :draft-key="`draft:${thread.id}`"
    />
  </div>
</template>
