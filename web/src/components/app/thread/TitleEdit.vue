<script setup lang="ts">
import { $t } from "@/i18n";
/** 重命名 in place: Enter or leaving the field saves, Esc cancels; an unchanged or empty title saves nothing. */
import { nextTick, onMounted, ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";

const props = defineProps<{ thread: ThreadView }>();
const emit = defineEmits<{ done: [] }>();
const title = ref(props.thread.title);
const input = ref<HTMLInputElement | null>(null);
let finished = false;
const save = useAction((t: string) => threadActions.update(props.thread.id, { title: t }), {
  invalidate: () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id)],
  success: $t("已重命名"),
});
function finish(keep: boolean) {
  if (finished) return;
  finished = true;
  const t = title.value.trim();
  if (keep && t && t !== props.thread.title) save.mutate(t);
  emit("done");
}
onMounted(() => nextTick(() => input.value?.select()));
</script>

<template>
  <input
    ref="input"
    v-model="title"
    maxlength="200"
    :aria-label="$t('重命名 #{no}', { no: thread.no })"
    class="h-7 min-w-0 flex-1 rounded-md border border-input bg-background px-1.5 text-sm font-medium outline-none focus:border-ring"
    @keydown.enter.prevent="finish(true)"
    @keydown.esc.prevent.stop="finish(false)"
    @blur="finish(true)"
    @click.stop
  />
</template>
