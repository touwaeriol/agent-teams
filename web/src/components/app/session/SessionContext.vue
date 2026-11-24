<script setup lang="ts">
/**
 * 上下文 of a session's 会话信息 panel: the total bar, "已使用 x% · 7.7万 / 100万" (the window is the
 * runtime's, never a fixed number). Hidden as a whole when the runtime reports no use (Antigravity)
 * or no turn ended yet.
 *
 * Extension points for the per-category breakdown: the `actions` slot sits right of the title (a
 * 刷新 button), the default slot under the bar (categories, MCP tools…); both get the total.
 */
import { computed } from "vue";
import type { ThreadSession } from "@/api/views";
import { INK } from "@/lib/present";
import ProgressBar from "../ProgressBar.vue";
import SectionLabel from "../SectionLabel.vue";
import { contextTotal } from "./session";

const props = defineProps<{ session: ThreadSession | null | undefined }>();
const total = computed(() => contextTotal(props.session));
/** Past 80 % the bar turns to the waiting colour: the session is close to compacting. */
const tone = computed(() => (total.value && total.value.pct >= 80 ? INK.wait : INK.doing));
defineExpose({ total });
</script>

<template>
  <section v-if="total" class="flex flex-col gap-2" data-testid="session-context">
    <div class="flex items-center justify-between">
      <SectionLabel>上下文</SectionLabel>
      <slot name="actions" :total="total" />
    </div>
    <span class="text-[13px]" data-testid="context-total">{{ total.text }}</span>
    <div class="flex" data-testid="context-bar"><ProgressBar :pct="`${total.pct}%`" :tone="tone" :height="6" /></div>
    <slot :total="total" />
  </section>
</template>
