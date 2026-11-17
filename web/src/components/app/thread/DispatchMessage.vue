<script setup lang="ts">
/**
 * The first message of a thread's chat: the brief the coordinator (or the user) handed the lead,
 * as Markdown, folded to its first lines when long. When the coordinator dispatched from one of
 * the user's project-chat messages, that message is quoted above the brief (text, thumbnails,
 * and a link back to it in the project chat — the same ?msg= jump as under a forwarded copy).
 */
import { computed, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import type { ThreadDispatch } from "@/api/views";
import Markdown from "@/components/Markdown.vue";
import { backLink } from "@/lib/forward";
import { clock } from "@/lib/utils";
import Dot from "../Dot.vue";
import MessageImages from "../MessageImages.vue";
import { BRIEF_LINES, briefFoldable, dispatchSpeaker } from "./detail";

const props = defineProps<{ dispatch: ThreadDispatch; pid: string; compact?: boolean }>();
const route = useRoute();
const open = ref(false);
const foldable = computed(() => briefFoldable(props.dispatch.brief));
const folded = computed(() => foldable.value && !open.value);
const back = computed(() => (props.dispatch.source ? backLink(route.path, props.pid, props.dispatch.source.message_id) : null));
const fade = "linear-gradient(to bottom, #000 55%, transparent)";
const foldStyle = computed(() =>
  folded.value ? { maxHeight: `${BRIEF_LINES * (props.compact ? 26 : 28)}px`, overflow: "hidden", maskImage: fade, WebkitMaskImage: fade } : undefined,
);
</script>

<template>
  <article class="flex flex-col gap-2.5" data-dispatch>
    <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
      <Dot :color="dispatch.opened_by === 'user' ? '#8A8F98' : '#C8622A'" :size="8" />
      {{ dispatchSpeaker(dispatch.opened_by) }} · {{ clock(dispatch.at) }}
    </span>
    <blockquote v-if="dispatch.source" class="flex flex-col gap-2 rounded-r-lg border-l-[3px] border-border bg-raised/60 py-2 pl-3 pr-3 text-[13px] leading-[22px] text-muted-foreground" data-dispatch-source>
      <span class="text-xs">你的原话</span>
      <p v-if="dispatch.source.content" class="line-clamp-3 whitespace-pre-wrap break-words text-foreground/80">{{ dispatch.source.content }}</p>
      <MessageImages :items="dispatch.source.attachments" align="start" small />
      <RouterLink v-if="back" :to="back" class="self-start text-xs hover:text-foreground hover:underline" data-dispatch-back>在统筹会话中查看原消息</RouterLink>
    </blockquote>
    <div :style="foldStyle" data-brief>
      <Markdown :source="dispatch.brief" :class="compact ? 'text-sm leading-[26px]' : 'text-[15px] leading-7'" />
    </div>
    <button
      v-if="foldable"
      type="button"
      class="self-start text-xs text-muted-foreground hover:text-foreground hover:underline"
      :aria-expanded="open"
      data-brief-toggle
      @click="open = !open"
    >
      {{ open ? "收起" : "展开" }}
    </button>
  </article>
</template>
