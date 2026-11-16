<script setup lang="ts">
// Asks before doing something that cannot be undone. Uncontrolled: renders its own trigger
// (default: a 删除 button; `icon-only` = a bare trash icon; the default slot replaces the label).
// Controlled (opened from a menu item): pass `v-model:open` and no trigger is drawn.
import { Trash2 } from "lucide-vue-next";
import { computed, ref } from "vue";

const props = withDefaults(
  defineProps<{ title: string; description?: string; confirmLabel?: string; variant?: "danger" | "primary" | "default"; size?: "small" | "default"; iconOnly?: string; open?: boolean }>(),
  { confirmLabel: "删除", variant: "danger", size: "default", open: undefined, description: undefined, iconOnly: undefined },
);
const emit = defineEmits<{ confirm: []; "update:open": [open: boolean] }>();
const local = ref(false);
const controlled = computed(() => props.open !== undefined);
const shown = computed({
  get: () => (controlled.value ? Boolean(props.open) : local.value),
  set: (v: boolean) => (controlled.value ? emit("update:open", v) : (local.value = v)),
});
function confirm() {
  shown.value = false;
  emit("confirm");
}
</script>

<template>
  <template v-if="!controlled">
    <button
      v-if="iconOnly"
      type="button"
      :aria-label="iconOnly"
      class="flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
      @click="shown = true"
    >
      <Trash2 class="size-4" />
    </button>
    <el-button v-else :type="variant === 'default' ? undefined : variant" :size="size" @click="shown = true">
      <slot><Trash2 /> {{ confirmLabel }}</slot>
    </el-button>
  </template>
  <el-dialog align-center v-model="shown" class="at-dialog w-[calc(100%-2rem)] max-w-lg" width="32rem" :show-close="false" append-to-body role="alertdialog">
    <template #header>
      <div class="flex flex-col gap-2">
        <h2 class="text-lg font-semibold">{{ title }}</h2>
        <p v-if="description" class="text-[13px] leading-5 text-muted-foreground">{{ description }}</p>
      </div>
    </template>
    <template #footer>
      <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <el-button @click="shown = false">取消</el-button>
        <el-button type="danger" class="!ml-0" @click="confirm">{{ confirmLabel }}</el-button>
      </div>
    </template>
  </el-dialog>
</template>
