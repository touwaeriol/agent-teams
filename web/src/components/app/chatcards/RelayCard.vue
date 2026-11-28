<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * A message between threads or with the coordinator (meta.relay), folded to one line like the
 * Claude app: "已向 #7 标题 发送 1 条消息 ⌄" on the sender's side, "来自 #3 标题 的 1 条消息 ⌄" on the
 * receiver's, a subthread's report "子讨论串 #7 标题 · 已完成", an opened subthread. Unfolding shows
 * the text; "查看讨论串" opens the other thread.
 */
import { ChevronDown, ChevronRight, GitBranchPlus, MessageSquareShare } from "lucide-vue-next";
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import type { MessageView } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import { relayHasBody, relayHeader, relayThreadNo } from "@/lib/relay";

const props = defineProps<{ message: MessageView; pid: string }>();
const relay = computed(() => props.message.meta.relay!);
const header = computed(() => relayHeader(relay.value));
const no = computed(() => relayThreadNo(relay.value));
const body = computed(() => relayHasBody(props.message));
const open = ref(false);
</script>

<template>
  <section :aria-label="$t('讨论串之间的消息')" class="flex flex-col gap-1.5 text-[13px]" :data-relay="relay.dir">
    <button v-if="body" type="button" :aria-expanded="open" class="flex items-center gap-1.5 self-start text-left text-muted-foreground hover:text-foreground" @click="open = !open">
      <MessageSquareShare aria-hidden="true" class="size-3.5 shrink-0" :stroke-width="1.8" />
      <span>{{ header }}</span>
      <ChevronDown v-if="open" class="size-3.5" />
      <ChevronRight v-else class="size-3.5" />
    </button>
    <span v-else class="flex items-center gap-1.5 text-muted-foreground">
      <GitBranchPlus aria-hidden="true" class="size-3.5 shrink-0" :stroke-width="1.8" />
      {{ header }}
    </span>
    <div v-if="body && open" class="ml-5 border-l-2 border-[#E7E4DD] pl-3 dark:border-border">
      <Markdown :source="message.content" class="text-sm leading-6" />
    </div>
    <RouterLink v-if="no != null && (open || !body)" :to="`/projects/${pid}/threads/${no}`" class="ml-5 self-start text-xs text-primary hover:underline">{{ $t("查看讨论串") }}</RouterLink>
  </section>
</template>
