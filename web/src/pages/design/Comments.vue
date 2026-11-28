<script setup lang="ts">
import { $t } from "@/i18n";
/** 批注: stored and sent to the employee who published it (or the thread's lead), with files attached like a chat message. */
import { computed } from "vue";
import { useAttachments } from "@/components/app/attachments";
import MessageImages from "@/components/app/MessageImages.vue";
import { deliverables as deliverableActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverableComments } from "@/api/queries";
import type { DeliverableView, ThreadView } from "@/api/views";
import Composer from "@/components/app/Composer.vue";
import { clock } from "@/lib/utils";

const props = defineProps<{ item: DeliverableView; thread?: ThreadView; author: string; version?: number }>();
const comments = useDeliverableComments(() => props.item.id);
const files = useAttachments(() => props.item.project_id, undefined, () => `comment:${props.item.id}`);
const add = useAction((v: { text: string; ids: string[] }) => deliverableActions.comment(props.item.id, v.text, props.version, v.ids), {
  invalidate: () => [keys.deliverableComments(props.item.id)],
});
const items = computed(() => comments.data.value?.items ?? []);
const to = computed(() => props.author || $t("发布的员工"));
const onSend = (text: string, ids: string[]) => add.mutateAsync({ text, ids });
</script>

<template>
  <aside :aria-label="$t('批注')" class="at-wrap flex min-w-0 max-w-full flex-[1_1_300px] flex-col gap-3.5 border-l bg-panel px-[18px] py-4 sm:max-w-[360px]">
    <span class="flex items-center justify-between">
      <span class="font-semibold">{{ $t("批注 · {itemsCount}", { itemsCount: items.length }) }}</span>
      <span class="text-xs text-muted-foreground">{{ $t("批注会转给 {to}", { to }) }}</span>
    </span>
    <p v-if="items.length === 0" class="text-[13px] leading-6 text-muted-foreground">{{ $t("看完写下意见，会直接发给 {to}，回复在讨论串里。", { to }) }}</p>
    <div v-for="(c, i) in items" :key="c.id" class="flex flex-col gap-1.5 rounded-[10px] border bg-background px-3 py-2.5">
      <span class="flex items-center gap-2 text-xs text-muted-foreground">
        <span class="flex size-[18px] items-center justify-center rounded-full bg-[#C8622A] text-[10px] font-bold text-white">{{ i + 1 }}</span>
        {{ $t("你 · {created_at}", { created_at: clock(c.created_at) }) }}{{ c.version ? $t(" · 第 {version} 版", { version: c.version }) : "" }}{{ c.sent_to ? $t(" · 已发给 {sent_to}", { sent_to: c.sent_to }) : $t(" · 未转发") }}
      </span>
      <span class="whitespace-pre-wrap text-[13px] leading-[21px]">{{ c.text }}</span>
      <MessageImages :items="c.attachments ?? undefined" align="start" small />
    </div>
    <div class="mt-auto flex flex-col gap-1.5">
      <Composer id="note" variant="inline" :label="$t('写批注')" :placeholder="$t('写批注，发给做这个的员工')" :busy="add.isPending.value" :attachments="files" :on-send="onSend" />
      <p v-if="!thread" class="text-xs text-muted-foreground">{{ $t("这个交付物不属于任何讨论串，批注只会保存，不会转发。") }}</p>
    </div>
  </aside>
</template>
