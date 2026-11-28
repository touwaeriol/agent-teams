<script setup lang="ts">
import { $t } from "@/i18n";
/** A role's model (any tier): a typed id with suggestions; "" = the project setting, then the built-in default. */
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import { emptyRoleModel, type Runtime } from "@/lib/models";

defineProps<{ runtime: Runtime; roles?: { tier: string; runtime: string; model?: string }[] }>();
const value = defineModel<string>({ required: true });
</script>

<template>
  <ModelPicker v-model="value" live :runtime="runtime" :roles="roles" :label="$t('模型')" :placeholder="emptyRoleModel(runtime)" class="at-select-36 at-text-13" />
</template>
