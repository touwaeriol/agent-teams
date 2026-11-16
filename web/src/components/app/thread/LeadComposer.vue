<script setup lang="ts">
/** The message box to the thread's lead; a closed note once the thread has finished. */
import { computed } from "vue";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { leadOf } from "@/lib/present";
import { cn } from "@/lib/utils";
import { useAttachments } from "../attachments";
import Composer from "../Composer.vue";
import { threadClosed } from "./detail";

const props = defineProps<{ pid: string; thread: ThreadView; class?: string }>();
const attachments = useAttachments(() => props.pid);
const lead = computed(() => leadOf(props.thread));
const send = useAction((v: { text: string; images: string[] }) => conversations.send(props.thread.id, v.text, false, v.images), {
  invalidate: () => [keys.messages(props.thread.id)],
});
const onSend = (text: string, images: string[]) => send.mutateAsync({ text, images });
</script>

<template>
  <div :class="cn('shrink-0', props.class)">
    <p v-if="threadClosed(thread)" class="rounded-xl bg-background px-3 py-2.5 text-center text-xs text-muted-foreground">
      讨论串已{{ thread.status === "archived" ? "归档" : "结束" }}，不能再给组长发消息
    </p>
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
