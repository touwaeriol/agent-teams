<script setup lang="ts">
/** A session's state at the end of its sidebar row: spinner while it works, green ✓ ring 已完成, grey ✓✓ 已解决, orange dot 等你处理 (node offline, session crashed). */
import { Check, CheckCheck, X } from "lucide-vue-next";
import { INK } from "@/lib/present";
import Spin from "../Spin.vue";
import { SESSION_MARK_LABEL, type SessionMark } from "./session";

defineProps<{ mark: SessionMark }>();
</script>

<template>
  <span class="flex size-4 shrink-0 items-center justify-center" role="img" :aria-label="SESSION_MARK_LABEL[mark]" :title="SESSION_MARK_LABEL[mark]" :data-session-mark="mark">
    <Spin v-if="mark === 'working' || mark === 'queued'" :color="mark === 'working' ? INK.doing : INK.gray" :size="12" />
    <span v-else-if="mark === 'finished'" class="flex size-[14px] items-center justify-center rounded-full border-[1.5px]" :style="{ borderColor: INK.done, color: INK.done }"><Check class="size-2.5" :stroke-width="3.5" /></span>
    <CheckCheck v-else-if="mark === 'resolved'" class="size-3.5" :style="{ color: INK.idle }" :stroke-width="2.5" />
    <span v-else-if="mark === 'attention'" class="at-halo size-2.5 rounded-full" :style="{ background: INK.wait }" />
    <span v-else-if="mark === 'paused'" class="size-[11px] rounded-full border-[1.5px] border-dashed" :style="{ borderColor: INK.faint }" />
    <X v-else class="size-3.5" :style="{ color: INK.bad }" :stroke-width="2.5" />
  </span>
</template>
