<script setup lang="ts">
import { $t } from "@/i18n";
/** The quiet note left when a session was rebuilt from a recap. */
import type { MessageView } from "@/api/views";

defineProps<{ message: MessageView }>();
</script>

<template>
  <p class="flex items-center justify-center gap-2 text-xs text-muted-foreground" :title="$t('{n} 的会话记录丢失，平台用对话摘要开了新会话', { n: message.meta.employee ?? message.author })">
    <span class="h-px w-8 bg-[#E7E4DD]" />
    {{ $t("会话已恢复") }}
    <span class="h-px w-8 bg-[#E7E4DD]" />
  </p>
</template>
