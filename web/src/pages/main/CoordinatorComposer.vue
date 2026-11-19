<script setup lang="ts">
import { Square } from "lucide-vue-next";
import { computed } from "vue";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { useSendMessage } from "@/api/pending";
import { useMe } from "@/api/queries";
import type { CoordinatorView } from "@/api/views";
import { useAttachments } from "@/components/app/attachments";
import Composer from "@/components/app/Composer.vue";
import { draftKey } from "@/lib/persist";
import { coordinatorBusy } from "./logic";

const props = defineProps<{ pid: string; coordinator?: CoordinatorView }>();
const cid = computed(() => props.coordinator?.conversation_id ?? "");
const me = useMe();
// Drafts (text, attachments, @ references) are kept per viewer and conversation across remounts.
const draft = computed(() => draftKey(me.data.value?.id ?? (me.isError.value ? "anon" : undefined), props.pid, cid.value));
const attachments = useAttachments(() => props.pid, undefined, draft);
// The message shows in the chat at once (api/pending.ts); the box does not wait for the server.
const send = useSendMessage(() => cid.value, { draft: () => draft.value });
const stop = useAction(() => conversations.interrupt(cid.value), { success: "已让统筹停下" });
const name = computed(() => props.coordinator?.employee.name ?? "leader");
</script>

<template>
  <div class="shrink-0 bg-background px-3 pb-3 sm:px-6 sm:pb-5">
    <Composer
      id="boss"
      class="mx-auto max-w-[700px]"
      :label="`给 ${name} 发消息`"
      :placeholder="`说需求或问进度，${name} 会开讨论串去做；/ 命令，@ 引用文件和讨论串`"
      :hint="`${name} 只分派、不改代码`"
      :attachments="attachments"
      :on-send="send"
      :assist="{ pid, cid, scope: 'coordinator' }"
      :draft-key="draft"
    >
      <template #extra>
        <button
          v-if="coordinatorBusy(coordinator)"
          type="button"
          aria-label="停止"
          class="flex size-8 items-center justify-center rounded-lg border border-input bg-white dark:bg-well"
          @click="stop.mutate()"
        >
          <Square class="size-3 fill-current" />
        </button>
      </template>
    </Composer>
  </div>
</template>
