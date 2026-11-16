<script setup lang="ts">
// Office board: the pixel office (src/office) for one project. It lives outside the project
// shell (full-bleed dark page), so it opens its own event stream and paints the body dark.
import { computed, onBeforeUnmount, onMounted } from "vue";
import { useRoute } from "vue-router";
import { errorText } from "@/api/mutations";
import { useOfficeSnapshot, useProject } from "@/api/queries";
import { useProjectEvents } from "@/api/sse";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { DARK } from "./office/logic";
import OfficeView from "./office/OfficeView.vue";

const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const snapshot = useOfficeSnapshot(pid);
useProjectEvents(pid);
let previous = "";
onMounted(() => {
  previous = document.body.style.background;
  document.body.style.background = DARK.page;
});
onBeforeUnmount(() => (document.body.style.background = previous));
</script>

<template>
  <EmptyState v-if="snapshot.error.value" title="办公室打不开" :description="errorText(snapshot.error.value)" />
  <div v-else-if="!snapshot.data.value || !project.data.value" class="h-full" :style="{ background: DARK.page }"><Loading class="h-full" /></div>
  <OfficeView
    v-else
    :key="pid"
    :pid="pid"
    :name="project.data.value.name"
    :snapshot="snapshot.data.value"
    :layout-id="snapshot.data.value.layout_id || project.data.value.office_layout"
  />
</template>
