<script setup lang="ts">
/**
 * The quiet bar under a session's last reply once its employee finished a round (已完成, no card
 * open): 问题解决了吗？ [已解决] ends the session (status done, the node session is released; the
 * next message reopens it), [还没有，继续说] only puts the cursor in the message box.
 */
import { CircleHelp } from "lucide-vue-next";

defineProps<{ pending?: boolean }>();
const emit = defineEmits<{ resolve: []; continue: [] }>();
</script>

<template>
  <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border bg-background px-3.5 py-2.5 text-[13px]" role="group" aria-label="确认是否解决" data-testid="session-confirm">
    <span class="flex items-center gap-1.5 text-muted-foreground"><CircleHelp class="size-4" />问题解决了吗？</span>
    <span class="ml-auto flex items-center gap-2">
      <el-button size="small" type="primary" :loading="pending" @click="emit('resolve')">已解决</el-button>
      <el-button size="small" @click="emit('continue')">还没有，继续说</el-button>
    </span>
  </div>
</template>
