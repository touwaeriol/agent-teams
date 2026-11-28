<script setup lang="ts">
import { $t } from "@/i18n";
/** 发送反馈: a note to the administrators about this thread, optionally with its conversation. */
import { ref } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import type { ThreadView } from "@/api/views";
import FormDialog from "@/components/common/FormDialog.vue";

const props = defineProps<{ thread: ThreadView }>();
const open = defineModel<boolean>("open", { required: true });
const text = ref("");
const include = ref(false);
const send = useAction(() => threadActions.feedback(props.thread.id, text.value.trim(), include.value), {
  success: $t("反馈已发送，谢谢"),
  onSuccess: () => (open.value = false),
});
</script>

<template>
  <FormDialog
    v-model:open="open"
    :title="$t('发送反馈')"
    :description="$t('关于 #{no} {title}。管理员会在“管理后台 → 反馈”里看到。', { no: thread.no, title: thread.title })"
    :submit-label="$t('发送')"
    :busy="send.isPending.value"
    :disabled="!text.trim()"
    @submit="send.mutate()"
  >
    <el-input v-model="text" type="textarea" :rows="5" maxlength="5000" show-word-limit :placeholder="$t('哪里不好用、出了什么错，或者你的建议')" :aria-label="$t('反馈内容')" />
    <el-checkbox v-model="include" :label="$t('包含讨论串内容（最近 100 条消息）')" />
  </FormDialog>
</template>
