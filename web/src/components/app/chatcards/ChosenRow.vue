<script setup lang="ts">
import { $t } from "@/i18n";
import { Check } from "lucide-vue-next";
import LetterBadge from "./LetterBadge.vue";

defineProps<{ badge: string; label: string; recommended?: boolean; description?: string }>();
</script>

<template>
  <div class="flex items-start gap-3 rounded-xl bg-[#F1EFEA] px-3 py-2.5">
    <LetterBadge>{{ badge }}</LetterBadge>
    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="flex flex-wrap items-center gap-2 text-sm font-medium">
        {{ label }}
        <span v-if="recommended" class="rounded-md bg-[#E4E1DA] px-1.5 py-px text-[11px] font-normal text-[#5E5B54]">{{ $t("推荐") }}</span>
      </span>
      <span v-if="description" class="text-[13px] leading-5 text-muted-foreground">{{ description }}</span>
    </span>
    <Check :aria-label="$t('已选')" class="mt-1.5 size-4 shrink-0 text-[#5E5B54]" :stroke-width="2" />
  </div>
</template>
