<script setup lang="ts">
import { $t } from "@/i18n";
import { ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";

const props = defineProps<{ thread: ThreadView }>();
const confirming = ref(false);
const update = useAction((v: boolean) => threadActions.update(props.thread.id, { auto_approve: v }), {
  invalidate: () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id)],
});
function change(e: Event) {
  const input = e.target as HTMLInputElement;
  if (input.checked) {
    input.checked = props.thread.auto_approve;
    confirming.value = true;
  } else update.mutate(false);
}
</script>

<template>
  <label class="flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-[13px]">
    <input type="checkbox" :checked="thread.auto_approve" class="size-4 accent-[#B42318]" @change="change" />
    {{ $t("本讨论串内全部放行") }}
  </label>
  <ConfirmAction
    v-model:open="confirming"
    :title="$t('本讨论串内全部放行？')"
    :description="$t('只影响设成“逐条问”的成员：打开后它们的操作也不再询问，当前等你处理的审批会一并放行（会记入审计）。')"
    :confirm-label="$t('全部放行')"
    @confirm="update.mutate(true)"
  />
</template>
