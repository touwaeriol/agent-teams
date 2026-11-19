<script setup lang="ts">
// Message box. `large`: the coordinator chat (textarea + hint row); `inline`: one-line box for the
// lead. Enter sends, Shift+Enter breaks; IME composition is respected. With `draftKey` the
// unsent text survives reloads (localStorage). Slots: `hint` (left of the action row), `extra`.
import { computed, ref, watch } from "vue";
import { useDraft } from "@/lib/persist";
import { toast } from "@/lib/toast";
import { cn } from "@/lib/utils";
import AttachButton from "./AttachButton.vue";
import AttachmentStrip from "./AttachmentStrip.vue";
import { attachmentHandlers, type Attachments } from "./attachments";
import SendButton from "./SendButton.vue";
import AssistPopup from "./composer/AssistPopup.vue";
import CommandPanel from "./composer/CommandPanel.vue";
import PlusMenu from "./composer/PlusMenu.vue";
import RefChips from "./composer/RefChips.vue";
import type { MessageRef } from "./composer/assist";
import { useAutoGrow } from "./composer/useAutoGrow";
import { useComposerAssist, type AssistOptions } from "./composer/useComposerAssist";
import { useVoiceInput } from "./voice/useVoiceInput";
import VoiceButton from "./voice/VoiceButton.vue";
import VoiceOverlay from "./voice/VoiceOverlay.vue";

const props = withDefaults(
  defineProps<{
    label: string;
    placeholder: string;
    onSend: (text: string, images: string[], refs: MessageRef[]) => Promise<unknown> | void;
    busy?: boolean;
    attachments?: Attachments;
    hint?: string;
    variant?: "large" | "inline";
    class?: string;
    id?: string;
    draftKey?: string;
    /** "/" commands, "@" references and the "+" menu (needs the project; the conversation for commands). */
    assist?: AssistOptions;
  }>(),
  { variant: "large", attachments: undefined, hint: undefined, class: undefined, id: undefined, draftKey: undefined, assist: undefined },
);

const text = useDraft(() => props.draftKey);
const dragging = ref(false);
const imageCount = computed(() => props.attachments?.items.value.filter((x) => x.id).length ?? 0);
const empty = computed(() => !text.value.trim() && imageCount.value === 0);
const uploading = computed(() => props.busy || Boolean(props.attachments?.uploading.value));
const inputId = computed(() => props.id ?? `composer-${props.label}`);
const drop = attachmentHandlers(props.attachments, (on) => (dragging.value = on));
const assist = useComposerAssist(() => props.assist, text, () => document.getElementById(inputId.value) as HTMLInputElement | HTMLTextAreaElement | null);
const ring = computed(() => (dragging.value ? "ring-2 ring-[#2F6FDB]/40 border-[#2F6FDB]" : ""));
// Voice input (only when the admin configured it): speech goes in at the cursor, grey until final.
const field = ref<HTMLInputElement | HTMLTextAreaElement | null>(null);
const voice = useVoiceInput(text, field);
const scroll = ref({ top: 0, left: 0 });
const onScroll = (e: Event) => {
  const el = e.target as HTMLElement;
  scroll.value = { top: el.scrollTop, left: el.scrollLeft };
};
// The large box grows with its text (2 lines up to ~40% of the window), then scrolls inside.
// It never squeezes the chat above it under 120px (short windows, attachments, the plan card).
const grow = useAutoGrow(field, text, { keepVisible: 120, onResize: (el) => (scroll.value = { top: el.scrollTop, left: el.scrollLeft }) });
// With a scrollbar the text wraps narrower; the overlay keeps the same right edge (px-4 = 16px).
watch([imageCount, () => assist.panel.value, () => props.attachments?.items.value.length, () => assist.picked.value.length], () => grow.update(), { flush: "post" });
const overlayStyle = computed(() => (grow.gutter.value ? { paddingRight: `${16 + grow.gutter.value}px` } : undefined));
const speaking = computed(() => (voice.parts.value ? "text-transparent caret-foreground" : ""));

async function send() {
  if (voice.active.value) await voice.finish();
  const value = text.value.trim();
  if (assist.intercept(value)) return void (text.value = "");
  if (empty.value || props.busy) return;
  const images = props.attachments ? props.attachments.ids() : [];
  if (images === null) return void toast.info("图片还在上传，稍等一下");
  text.value = "";
  try {
    await props.onSend(value, images, assist.takeRefs(value));
    props.attachments?.clear();
    assist.clearRefs();
  } catch {
    text.value = value;
  }
}

function onKey(e: KeyboardEvent) {
  if (assist.onKeyDown(e)) return;
  if (voice.onKeyDown(e)) return;
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  void send();
}
</script>

<template>
  <form v-if="variant === 'inline'" :class="cn('relative flex flex-col gap-2 rounded-xl border border-[#E2DFD8] bg-panel py-1.5 pr-1.5', assist.enabled.value || attachments ? 'pl-1.5' : 'pl-3', ring, props.class)" v-on="drop" @submit.prevent="send">
    <AssistPopup v-if="assist.enabled.value" :assist="assist" />
    <CommandPanel v-if="assist.panel.value && props.assist" :assist="assist" :panel="assist.panel.value" v-bind="props.assist" class="!mx-0 !mt-1.5 mr-1.5" />
    <AttachmentStrip v-if="attachments" :a="attachments" class="pl-1.5 pt-1" />
    <RefChips :assist="assist" class="pl-1.5 pt-1" />
    <span class="flex items-center gap-2">
      <PlusMenu v-if="assist.enabled.value" :assist="assist" :a="attachments" :size="30" />
      <AttachButton v-else-if="attachments" :a="attachments" :size="30" />
      <label :for="inputId" class="sr-only">{{ label }}</label>
      <span class="relative flex min-w-0 flex-1">
        <input
          :id="inputId"
          ref="field"
          v-model="text"
          :placeholder="placeholder"
          :readonly="voice.active.value"
          :class="cn('min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground/80', speaking)"
          @keydown="onKey"
          @keyup="voice.onKeyUp"
          @scroll="onScroll"
        />
        <VoiceOverlay v-if="voice.parts.value" :parts="voice.parts.value" :scroll-top="0" :scroll-left="scroll.left" class="flex items-center whitespace-pre text-[13px]" />
      </span>
      <VoiceButton v-if="voice.enabled.value" :v="voice" :size="30" />
      <SendButton :label="label" :busy="uploading" :disabled="empty" :size="30" />
    </span>
  </form>
  <form v-else :class="cn('relative rounded-2xl border border-[#E2DFD8] bg-panel shadow-[0_2px_12px_-4px_rgba(0,0,0,0.08)]', ring, props.class)" v-on="drop" @submit.prevent="send">
    <AssistPopup v-if="assist.enabled.value" :assist="assist" />
    <CommandPanel v-if="assist.panel.value && props.assist" :assist="assist" :panel="assist.panel.value" v-bind="props.assist" />
    <AttachmentStrip v-if="attachments" :a="attachments" class="px-4 pt-3" />
    <RefChips :assist="assist" class="px-4 pt-3" />
    <label :for="inputId" class="sr-only">{{ label }}</label>
    <div class="relative">
      <textarea
        :id="inputId"
        ref="field"
        v-model="text"
        rows="2"
        :placeholder="placeholder"
        :readonly="voice.active.value"
        :class="cn('block w-full resize-none bg-transparent px-4 pt-3.5 text-[15px] leading-6 outline-none placeholder:text-muted-foreground/80', speaking)"
        @keydown="onKey"
        @keyup="voice.onKeyUp"
        @scroll="onScroll"
      />
      <VoiceOverlay
        v-if="voice.parts.value"
        :parts="voice.parts.value"
        :scroll-top="scroll.top"
        :scroll-left="0"
        :style="overlayStyle"
        class="whitespace-pre-wrap break-words px-4 pt-3.5 text-[15px] leading-6"
      />
    </div>
    <div class="flex items-center justify-between gap-2 px-3 pb-3 pt-1.5">
      <span class="flex min-w-0 items-center gap-2">
        <PlusMenu v-if="assist.enabled.value" :assist="assist" :a="attachments" />
        <span class="text-xs text-muted-foreground"><slot name="hint">{{ hint }}</slot></span>
      </span>
      <span class="flex items-center gap-2">
        <AttachButton v-if="attachments && !assist.enabled.value" :a="attachments" />
        <slot name="extra" />
        <VoiceButton v-if="voice.enabled.value" :v="voice" />
        <SendButton label="发送" :busy="uploading" :disabled="empty" />
      </span>
    </div>
  </form>
</template>
