<script setup lang="ts">
// Message box. `large`: the coordinator chat (textarea + hint row); `inline`: one-line box for the
// lead. Enter sends, Shift+Enter breaks; IME composition is respected. With `draftKey` the
// unsent text survives reloads (localStorage). Slots: `hint` (left of the action row), `extra`.
import { computed, ref } from "vue";
import { useDraft } from "@/lib/persist";
import { toast } from "@/lib/toast";
import { cn } from "@/lib/utils";
import AttachButton from "./AttachButton.vue";
import AttachmentStrip from "./AttachmentStrip.vue";
import { attachmentHandlers, type Attachments } from "./attachments";
import SendButton from "./SendButton.vue";

const props = withDefaults(
  defineProps<{
    label: string;
    placeholder: string;
    onSend: (text: string, images: string[]) => Promise<unknown> | void;
    busy?: boolean;
    attachments?: Attachments;
    hint?: string;
    variant?: "large" | "inline";
    class?: string;
    id?: string;
    draftKey?: string;
  }>(),
  { variant: "large", attachments: undefined, hint: undefined, class: undefined, id: undefined, draftKey: undefined },
);

const text = useDraft(() => props.draftKey);
const dragging = ref(false);
const imageCount = computed(() => props.attachments?.items.value.filter((x) => x.id).length ?? 0);
const empty = computed(() => !text.value.trim() && imageCount.value === 0);
const uploading = computed(() => props.busy || Boolean(props.attachments?.uploading.value));
const inputId = computed(() => props.id ?? `composer-${props.label}`);
const drop = attachmentHandlers(props.attachments, (on) => (dragging.value = on));
const ring = computed(() => (dragging.value ? "ring-2 ring-[#2F6FDB]/40 border-[#2F6FDB]" : ""));

async function send() {
  const value = text.value.trim();
  if (empty.value || props.busy) return;
  const images = props.attachments ? props.attachments.ids() : [];
  if (images === null) return void toast.info("图片还在上传，稍等一下");
  text.value = "";
  try {
    await props.onSend(value, images);
    props.attachments?.clear();
  } catch {
    text.value = value;
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
  e.preventDefault();
  void send();
}
</script>

<template>
  <form v-if="variant === 'inline'" :class="cn('flex flex-col gap-2 rounded-xl border border-[#E2DFD8] bg-panel py-1.5 pl-3 pr-1.5', ring, props.class)" v-on="drop" @submit.prevent="send">
    <AttachmentStrip v-if="attachments" :a="attachments" class="pt-1" />
    <span class="flex items-center gap-2">
      <label :for="inputId" class="sr-only">{{ label }}</label>
      <input
        :id="inputId"
        v-model="text"
        :placeholder="placeholder"
        class="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground/80"
        @keydown="onKey"
      />
      <AttachButton v-if="attachments" :a="attachments" :size="30" />
      <SendButton :label="label" :busy="uploading" :disabled="empty" :size="30" />
    </span>
  </form>
  <form v-else :class="cn('rounded-2xl border border-[#E2DFD8] bg-panel shadow-[0_2px_12px_-4px_rgba(0,0,0,0.08)]', ring, props.class)" v-on="drop" @submit.prevent="send">
    <AttachmentStrip v-if="attachments" :a="attachments" class="px-4 pt-3" />
    <label :for="inputId" class="sr-only">{{ label }}</label>
    <textarea
      :id="inputId"
      v-model="text"
      rows="2"
      :placeholder="placeholder"
      class="block w-full resize-none bg-transparent px-4 pt-3.5 text-[15px] leading-6 outline-none placeholder:text-muted-foreground/80"
      @keydown="onKey"
    />
    <div class="flex items-center justify-between gap-2 px-3 pb-3 pt-1.5">
      <span class="text-xs text-muted-foreground"><slot name="hint">{{ hint }}</slot></span>
      <span class="flex items-center gap-2">
        <AttachButton v-if="attachments" :a="attachments" />
        <slot name="extra" />
        <SendButton label="发送" :busy="uploading" :disabled="empty" />
      </span>
    </div>
  </form>
</template>
