<script setup lang="ts">
import { $t } from "@/i18n";
/** Read-only text to copy (commands, tokens). */
import { Check, Copy } from "lucide-vue-next";
import { ref } from "vue";
import { cn } from "@/lib/utils";
import { toast } from "@/lib/toast";

const props = defineProps<{ text: string; class?: string }>();
const copied = ref(false);
async function copy() {
  await navigator.clipboard.writeText(props.text);
  copied.value = true;
  toast.success($t("已复制"));
  setTimeout(() => (copied.value = false), 1500);
}
</script>

<template>
  <div :class="cn('relative rounded-lg border bg-well', props.class)">
    <pre class="scroll-thin overflow-x-auto whitespace-pre-wrap break-all p-3 pr-12 font-mono text-xs leading-5">{{ text }}</pre>
    <button
      type="button"
      :aria-label="$t('复制')"
      class="absolute right-1.5 top-1.5 flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
      @click="copy"
    >
      <Check v-if="copied" class="size-4" />
      <Copy v-else class="size-4" />
    </button>
  </div>
</template>
