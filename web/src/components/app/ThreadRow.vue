<script setup lang="ts">
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { isWorking, threadActive } from "@/lib/live";
import LiveDot from "./LiveDot.vue";
import ThreadMark from "./ThreadMark.vue";
import ThreadMenu from "./ThreadMenu.vue";

const props = defineProps<{ thread: ThreadView; line: string; menu: boolean; live?: boolean }>();
const emit = defineEmits<{ open: [no: number] }>();
const working = computed(() => Boolean(props.live) && threadActive(props.thread) && props.thread.members.some(isWorking));
</script>

<template>
  <div class="group -mx-2 flex items-start gap-1 rounded-lg hover:bg-background">
    <button type="button" class="flex min-w-0 flex-1 gap-2.5 p-2 text-left" @click="emit('open', thread.no)">
      <ThreadMark :thread="thread" />
      <span class="flex min-w-0 flex-col gap-[3px]">
        <span class="text-sm font-medium">#{{ thread.no }} {{ thread.title }}</span>
        <span class="flex items-start gap-1.5 text-xs leading-[18px] text-muted-foreground">
          <LiveDot v-if="working" color="#2F6FDB" :on="true" :size="6" class="mt-[6px]" />
          <span class="line-clamp-2">{{ line }}</span>
        </span>
      </span>
    </button>
    <ThreadMenu
      v-if="menu"
      :thread="thread"
      class="mt-1.5 shrink-0 opacity-0 focus-visible:opacity-100 group-hover:opacity-100 has-[[aria-expanded=true]]:opacity-100 max-lg:opacity-100"
    />
  </div>
</template>
