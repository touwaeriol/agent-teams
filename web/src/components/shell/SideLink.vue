<script setup lang="ts">
import { RouterLink } from "vue-router";
import { cn } from "@/lib/utils";

defineProps<{ to: string; strong?: boolean }>();
const emit = defineEmits<{ navigate: [] }>();
</script>

<template>
  <RouterLink v-slot="{ href, navigate, isExactActive }" :to="to" custom>
    <a
      :href="href"
      :class="cn('flex h-9 items-center gap-2.5 rounded-lg px-2.5 [&_svg]:size-4', strong && 'font-medium', isExactActive ? 'bg-accent' : 'hover:bg-accent/60')"
      @click="
        (e) => {
          navigate(e);
          emit('navigate');
        }
      "
    >
      <slot name="icon" />
      <slot />
    </a>
  </RouterLink>
</template>
