<script setup lang="ts">
/** Images of a sent message, as thumbnails that open the full image. */
import type { AttachmentRef } from "@/api/views";
import { attachmentUrl } from "@/lib/images";
import { cn } from "@/lib/utils";

withDefaults(defineProps<{ items: AttachmentRef[] | undefined; align?: "start" | "end" }>(), { align: "end" });
</script>

<template>
  <div v-if="items?.length" :class="cn('flex flex-wrap gap-2', align === 'end' ? 'justify-end self-end' : 'self-start')">
    <a v-for="x in items" :key="x.id" :href="attachmentUrl(x.id)" target="_blank" rel="noreferrer" class="block size-24 overflow-hidden rounded-xl border bg-raised" :title="x.name">
      <img :src="attachmentUrl(x.id)" :alt="x.name" loading="lazy" class="size-full object-cover" />
    </a>
  </div>
</template>
