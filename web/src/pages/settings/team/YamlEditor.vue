<script setup lang="ts">
import { $t } from "@/i18n";
/** The role templates as YAML (coordinator / lead / members, docs/threads.md format). */
import { ref, watch } from "vue";
import { roles as roleActions } from "@/api/actions";
import { errorText, useAction } from "@/api/mutations";
import { keys, useRolesYaml } from "@/api/queries";
import Callout from "@/components/common/Callout.vue";
import Loading from "@/components/common/Loading.vue";
import Spinner from "@/components/common/Spinner.vue";

const yaml = useRolesYaml(true);
const text = ref("");
watch(() => yaml.data.value, (d) => d && (text.value = d.yaml), { immediate: true });
const save = useAction(() => roleActions.saveYaml(text.value), { invalidate: [keys.roles, keys.rolesYaml], success: $t("已保存") });
</script>

<template>
  <Loading v-if="yaml.isLoading.value" />
  <section v-else class="flex flex-col gap-3 rounded-[14px] border bg-panel p-5">
    <p class="text-[13px] text-muted-foreground">{{ $t("岗位模板的 YAML（coordinator / lead / members 格式，见 docs/threads.md）。") }}</p>
    <Callout v-if="yaml.error.value" tone="bad">{{ errorText(yaml.error.value) }}</Callout>
    <el-input v-model="text" type="textarea" :rows="22" class="at-mono at-yaml" :spellcheck="false" :aria-label="$t('岗位模板 YAML')" />
    <div class="flex justify-end">
      <el-button type="primary" :disabled="save.isPending.value || text === yaml.data.value?.yaml" @click="save.mutate()"><Spinner v-if="save.isPending.value" /> {{ $t("保存 YAML") }}</el-button>
    </div>
  </section>
</template>
