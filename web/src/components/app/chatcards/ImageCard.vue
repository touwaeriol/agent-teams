<script setup lang="ts">
/**
 * Screenshots an employee uploaded (publish_file with images): images are not deliverables, the
 * server keeps them as thread attachments and posts this message. Who, the line, then thumbnails.
 */
import type { MessageView } from "@/api/views";
import { clock } from "@/lib/utils";
import MessageImages from "../MessageImages.vue";

defineProps<{ message: MessageView }>();
</script>

<template>
  <div class="flex flex-col gap-2" data-image-card>
    <span class="text-xs text-muted-foreground">{{ message.meta.employee ?? message.author }} · {{ clock(message.created_at) }}</span>
    <p v-if="message.content" class="text-sm leading-6">{{ message.content }}</p>
    <MessageImages :items="message.meta.attachments" align="start" />
  </div>
</template>
