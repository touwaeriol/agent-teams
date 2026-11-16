<script setup lang="ts">
// Migration scaffold: a route whose page has not been ported from web/ yet. It names the React
// source so the route table stays complete while pages move over one by one (README.md).
import { computed } from "vue";
import { useRoute } from "vue-router";
import EmptyState from "@/components/common/EmptyState.vue";
import NavButton from "@/components/shell/NavButton.vue";

const route = useRoute();
const source = computed(() => String(route.meta.port ?? ""));
</script>

<template>
  <div class="p-4">
    <NavButton />
    <div class="mx-auto max-w-xl pt-16">
      <EmptyState title="这个页面还在迁移" :description="`Vue 版还没有这个页面；React 版见 ${source}。`" />
    </div>
  </div>
</template>
