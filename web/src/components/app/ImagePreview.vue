<script setup lang="ts">
/**
 * In-page image preview: a full-screen overlay over the current page (never a new tab). Wraps
 * Element Plus's ElImageViewer: Esc or a click on the mask closes; ←/→ and the arrow buttons switch
 * between images; the toolbar buttons, ↑/↓ and the mouse wheel zoom; "2 / 5" shows the position.
 * Shown while `index` is a number (the image to start at); emits `close` to reset it to null.
 */
defineProps<{ urls: string[]; index: number | null }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <el-image-viewer
    v-if="index !== null && urls.length > 0"
    :url-list="urls"
    :initial-index="Math.min(Math.max(index, 0), urls.length - 1)"
    teleported
    hide-on-click-modal
    show-progress
    :infinite="false"
    @close="emit('close')"
  />
</template>
