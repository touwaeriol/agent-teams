<script setup lang="ts">
// The first lines of a published Markdown document, rendered small for a preview card. The card
// itself is the link: inside, links render as plain text, images are left out (not fetched) and the
// whole preview is inert. The text is cached per signed URL by vue-query, so folding the strip or
// coming back to the page does not fetch it again. While loading or on failure: the slot (placeholder).
import { useQuery } from "@tanstack/vue-query";
import { computed } from "vue";
import Markdown from "@/components/Markdown.vue";
import { markdownUrl } from "@/lib/artifacts";

const props = defineProps<{ url: string; baseUrl: string }>();
const text = useQuery({
  queryKey: computed(() => ["markdown-peek", props.url]),
  queryFn: async () => {
    const r = await fetch(props.url, { credentials: "omit", referrerPolicy: "no-referrer" });
    if (!r.ok) throw new Error(String(r.status));
    // only the head is visible; keep the render cheap for long documents
    return (await r.text()).slice(0, 1200);
  },
  staleTime: Infinity,
  gcTime: 30 * 60_000,
  retry: false,
});
</script>

<template>
  <span v-if="text.data.value === undefined" class="block" aria-hidden="true"><slot /></span>
  <span v-else class="block w-[250%] origin-top-left scale-[0.4]" aria-hidden="true" inert>
    <Markdown :source="text.data.value" :url-transform="markdownUrl(baseUrl)" preview class="pointer-events-none leading-5" />
  </span>
</template>
