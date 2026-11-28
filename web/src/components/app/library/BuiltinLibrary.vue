<script setup lang="ts">
// Admin page: built-in plugins (disabled ones included) and admin-created built-in skills.
import { $t } from "@/i18n";
import { computed } from "vue";
import { admin as adminActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminPlugins } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import BuiltinSkills from "./BuiltinSkills.vue";
import LibrarySection from "./LibrarySection.vue";
import PluginActions from "./PluginActions.vue";
import PluginGroup from "./PluginGroup.vue";
import PluginRow from "./PluginRow.vue";
import ZipButton from "./ZipButton.vue";

const HELP = $t("所有用户都能在岗位上勾选内置插件，它会运行在用户自己的节点上。停用后用户看不到它，已勾选的岗位也不再加载。");
const list = useAdminPlugins();
const plugins = computed(() => list.data.value?.plugins ?? []);
const invalidate = [keys.adminPlugins, keys.plugins, keys.roles];
const upload = useAction((zip: string) => adminActions.uploadPlugin(zip), { invalidate: [keys.adminPlugins, keys.plugins], success: $t("内置插件已上传") });
const toggle = useAction(({ id, on }: { id: string; on: boolean }) => adminActions.setPluginEnabled(id, on), { invalidate });
const remove = useAction((id: string) => adminActions.removePlugin(id), { invalidate, success: $t("已删除") });
</script>

<template>
  <LibrarySection :title="$t('内置插件')" :label="$t('内置插件和技能')" :help="HELP">
    <template #actions>
      <ZipButton :label="$t('上传内置插件')" :pending="upload.isPending.value" @zip="(zip: string) => upload.mutate(zip)" />
    </template>
    <Loading v-if="list.isLoading.value" />
    <PluginGroup :title="$t('插件')" :empty="$t('还没有内置插件')" :count="plugins.length">
      <PluginRow v-for="p in plugins" :key="p.id" :plugin="p">
        <PluginActions :plugin="p" @toggle="(on: boolean) => toggle.mutate({ id: p.id, on })" @remove="remove.mutate(p.id)" />
      </PluginRow>
    </PluginGroup>
    <BuiltinSkills />
  </LibrarySection>
</template>
