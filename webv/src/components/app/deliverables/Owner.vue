<script setup lang="ts">
import { computed } from "vue";
import { ownerLabel } from "@/lib/library";
import Initial from "../Initial.vue";

const props = defineProps<{ by: string }>();
const name = computed(() => ownerLabel(props.by));
</script>

<template>
  <span class="flex min-w-0 items-center gap-1.5">
    <Initial :name="name" :bg="by === 'user' ? '#F4E3D7' : '#ECEAE4'" :fg="by === 'user' ? '#A24E1E' : '#4A4843'" :size="18" round />
    <span class="truncate">{{ name }}</span>
  </span>
</template>
