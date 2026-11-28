<script setup lang="ts">
import { $t } from "@/i18n";
/** The header's ✓: archive the thread (asks first while it is still going); hidden once archived. */
import { CircleCheck } from "lucide-vue-next";
import { computed, ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import IconButton from "@/components/common/IconButton.vue";
import { threadActive } from "@/lib/live";
import { archiveNotice } from "./threadMenu";

const props = defineProps<{ thread: ThreadView }>();
const confirming = ref(false);
const invalidate = () => [keys.threads(props.thread.project_id), keys.thread(props.thread.id), keys.waiting(props.thread.project_id)];
const archive = useAction(() => threadActions.archive(props.thread.id), { invalidate, success: $t("#{no} 已归档", { no: props.thread.no }) });
const notice = computed(() => archiveNotice(props.thread.no));
const click = () => (threadActive(props.thread) ? (confirming.value = true) : archive.mutate());
</script>

<template>
  <template v-if="thread.status !== 'archived'">
    <IconButton :label="$t('归档讨论串')" :disabled="archive.isPending.value" @click="click"><CircleCheck :stroke-width="1.8" /></IconButton>
    <ConfirmAction v-model:open="confirming" :title="notice.title" :description="notice.description" :confirm-label="$t('归档')" variant="default" @confirm="archive.mutate()" />
  </template>
</template>
