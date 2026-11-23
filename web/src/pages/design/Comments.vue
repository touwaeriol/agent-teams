<script setup lang="ts">
/** 批注: stored and sent to the employee who published it (or the thread's lead). */
import { computed } from "vue";
import { deliverables as deliverableActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useDeliverableComments } from "@/api/queries";
import type { DeliverableView, ThreadView } from "@/api/views";
import Composer from "@/components/app/Composer.vue";
import { clock } from "@/lib/utils";

const props = defineProps<{ item: DeliverableView; thread?: ThreadView; author: string; version?: number }>();
const comments = useDeliverableComments(() => props.item.id);
const add = useAction((text: string) => deliverableActions.comment(props.item.id, text, props.version), {
  invalidate: () => [keys.deliverableComments(props.item.id)],
});
const items = computed(() => comments.data.value?.items ?? []);
const to = computed(() => props.author || "发布的员工");
const onSend = (text: string) => add.mutateAsync(text);
</script>

<template>
  <aside aria-label="批注" class="at-wrap flex min-w-0 max-w-full flex-[1_1_300px] flex-col gap-3.5 border-l bg-panel px-[18px] py-4 sm:max-w-[360px]">
    <span class="flex items-center justify-between">
      <span class="font-semibold">批注 · {{ items.length }}</span>
      <span class="text-xs text-muted-foreground">批注会转给 {{ to }}</span>
    </span>
    <p v-if="items.length === 0" class="text-[13px] leading-6 text-muted-foreground">看完写下意见，会直接发给 {{ to }}，回复在讨论串里。</p>
    <div v-for="(c, i) in items" :key="c.id" class="flex flex-col gap-1.5 rounded-[10px] border bg-background px-3 py-2.5">
      <span class="flex items-center gap-2 text-xs text-muted-foreground">
        <span class="flex size-[18px] items-center justify-center rounded-full bg-[#C8622A] text-[10px] font-bold text-white">{{ i + 1 }}</span>
        你 · {{ clock(c.created_at) }}{{ c.version ? ` · 第 ${c.version} 版` : "" }}{{ c.sent_to ? ` · 已发给 ${c.sent_to}` : " · 未转发" }}
      </span>
      <span class="whitespace-pre-wrap text-[13px] leading-[21px]">{{ c.text }}</span>
    </div>
    <div class="mt-auto flex flex-col gap-1.5">
      <Composer id="note" variant="inline" label="写批注" placeholder="写批注，发给做这个的员工" :busy="add.isPending.value" :on-send="onSend" />
      <p v-if="!thread" class="text-xs text-muted-foreground">这个交付物不属于任何讨论串，批注只会保存，不会转发。</p>
    </div>
  </aside>
</template>
