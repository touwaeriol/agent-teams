<script setup lang="ts">
/** Icon of an attached file by kind: image, PDF, text, archive or any other file. */
import { computed } from "vue";
import { File, FileArchive, FileImage, FileText } from "lucide-vue-next";

const props = defineProps<{ kind: "image" | "pdf" | "text" | "file"; name?: string }>();
const ARCHIVES = /\.(zip|tar|gz|tgz|bz2|xz|7z|rar|zst)$/i;
const icon = computed(() => (props.kind === "image" ? FileImage : props.kind === "pdf" || props.kind === "text" ? FileText : ARCHIVES.test(props.name ?? "") ? FileArchive : File));
const tone = computed(() => (props.kind === "pdf" ? "text-red-500" : "text-muted-foreground"));
</script>

<template>
  <component :is="icon" :class="tone" :stroke-width="1.6" aria-hidden="true" />
</template>
