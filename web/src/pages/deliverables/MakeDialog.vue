<script setup lang="ts">
import { $t } from "@/i18n";
/** “做一个…”: a templated request handed to the coordinator, which opens a thread for it. */
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import Field from "@/components/common/Field.vue";
import FormDialog from "@/components/common/FormDialog.vue";
import { deliverableKind } from "@/lib/present";

const props = defineProps<{ pid: string; kind: string | null }>();
const emit = defineEmits<{ close: [] }>();
const router = useRouter();
const brief = ref("");
const label = computed(() => (props.kind ? deliverableKind(props.kind).label : ""));
const open = computed({ get: () => props.kind !== null, set: (o: boolean) => !o && emit("close") });
const request = useAction(() => projectActions.requestDeliverable(props.pid, props.kind ?? "", brief.value.trim()), {
  success: $t("已交给项目统筹"),
  onSuccess: () => {
    brief.value = "";
    emit("close");
    void router.push(`/projects/${props.pid}`);
  },
});
</script>

<template>
  <FormDialog
    v-model:open="open"
    :title="$t('做一个{label}', { label })"
    :description="$t('会作为一条消息发给项目统筹，它决定怎么开讨论串去做。')"
    :submit-label="$t('交给统筹')"
    :busy="request.isPending.value"
    :disabled="!brief.trim()"
    @submit="request.mutate()"
  >
    <Field v-slot="{ id }" :label="$t('要做什么')">
      <el-input :id="id" v-model="brief" type="textarea" :rows="4" :placeholder="$t('例如：给投资人的 v2 进展介绍')" />
    </Field>
  </FormDialog>
</template>
