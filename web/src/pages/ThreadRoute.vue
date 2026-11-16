<script setup lang="ts">
// /projects/:pid/threads/:no(/:tab): on wide screens the main board with this thread open in the
// right panel (the tab names a view open over the chat); narrower screens get the thread page.
import { computed } from "vue";
import { useRoute } from "vue-router";
import { detailViewOf } from "@/components/app/thread/detail";
import { useMedia } from "@/lib/media";
import Main from "./Main.vue";
import ThreadPage from "./ThreadPage.vue";

const route = useRoute();
const wide = useMedia("(min-width: 1024px)");
const openNo = computed(() => Number(route.params.no) || null);
const view = computed(() => detailViewOf(route.params.tab));
</script>

<template>
  <Main v-if="wide" :open-no="openNo" :view="view" />
  <ThreadPage v-else />
</template>
