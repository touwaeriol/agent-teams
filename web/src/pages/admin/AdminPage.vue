<script setup lang="ts">
/** Frame of every admin console page: the phone ☰ button, the title, an optional line under it, optional buttons on the right (#actions), then the content. */
import NavButton from "@/components/shell/NavButton.vue";

defineProps<{ title: string; description?: string }>();
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[980px] flex-col gap-5">
      <div class="flex items-start gap-1">
        <NavButton />
        <div class="flex min-w-0 flex-1 flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">{{ title }}</h1>
          <p v-if="description" class="text-[13px] text-muted-foreground">{{ description }}</p>
        </div>
        <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2"><slot name="actions" /></div>
      </div>
      <slot />
    </div>
  </main>
</template>
