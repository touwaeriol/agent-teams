<script setup lang="ts">
// Deliverables board (资料库, full page): the library table, plus the “新做一个” chips that
// hand a templated request to the coordinator.
import { computed, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { useProject } from "@/api/queries";
import LibraryBrowser from "@/components/app/deliverables/LibraryBrowser.vue";
import NavButton from "@/components/shell/NavButton.vue";
import { deliverableKind } from "@/lib/present";
import MakeDialog from "./deliverables/MakeDialog.vue";

const MAKE = ["design", "doc", "slides", "dashboard", "board"].map((k) => ({ kind: k, look: deliverableKind(k) }));
const route = useRoute();
const pid = computed(() => String(route.params.pid ?? ""));
const project = useProject(pid);
const making = ref<string | null>(null);
</script>

<template>
  <main class="flex min-h-full flex-col px-4 pb-6 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex w-full max-w-[1080px] flex-1 flex-col gap-5">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1">
          <RouterLink :to="`/projects/${pid}`" class="text-xs text-muted-foreground hover:text-foreground">{{ project.data.value?.name ?? "项目" }}</RouterLink>
          <h1 class="text-[22px] font-semibold">资料库</h1>
        </div>
      </div>
      <section class="flex flex-col gap-2">
        <span class="text-xs text-muted-foreground">让团队做一个新的（交给项目统筹，它会开讨论串去做）</span>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="m in MAKE"
            :key="m.kind"
            type="button"
            class="flex h-9 items-center gap-2 rounded-lg border bg-panel px-3 text-[13px] hover:border-input"
            @click="making = m.kind"
          >
            <span class="flex size-5 items-center justify-center rounded text-[11px] font-bold" :style="{ background: m.look.bg, color: m.look.fg }">{{ m.look.glyph }}</span>
            {{ m.look.label }}
          </button>
        </div>
      </section>
      <section class="flex min-h-[420px] flex-1 flex-col rounded-[14px] border bg-panel px-4 pt-3">
        <LibraryBrowser :pid="pid" variant="page" />
      </section>
    </div>
    <MakeDialog :pid="pid" :kind="making" @close="making = null" />
  </main>
</template>
