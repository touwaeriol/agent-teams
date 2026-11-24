<script setup lang="ts">
/** One quiet line at the bottom while the thread is going. */
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import Dot from "../Dot.vue";
import Spin from "../Spin.vue";
import { sessionText } from "../session/session";
import { typingLine } from "./detail";

const props = defineProps<{ thread: ThreadView; session?: boolean }>();
const line = computed(() => {
  const l = typingLine(props.thread);
  return l && props.session ? { ...l, text: sessionText(l.text) } : l;
});
</script>

<template>
  <p v-if="line" role="status" class="flex items-center gap-2 text-xs text-muted-foreground">
    <Spin v-if="line.spin" color="#8A877F" :size="11" />
    <Dot v-else color="#B7B3AA" :size="6" class="mx-0.5" />
    {{ line.text }}
  </p>
</template>
