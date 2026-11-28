<script setup lang="ts">
// 插件库 on the Team page: my plugins (enable / delete) and the enabled built-ins. A plugin is a
// Claude Code plugin package uploaded as a zip; uploading the same name again adds a version.
import { $t } from "@/i18n";
import { computed } from "vue";
import { plugins as pluginActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, usePlugins } from "@/api/queries";
import Loading from "@/components/common/Loading.vue";
import { splitLibrary } from "@/lib/plugins";
import LibrarySection from "./LibrarySection.vue";
import PluginActions from "./PluginActions.vue";
import PluginGroup from "./PluginGroup.vue";
import PluginRow from "./PluginRow.vue";
import { PLUGIN_HELP } from "./upload";
import ZipButton from "./ZipButton.vue";

const list = usePlugins();
const groups = computed(() => splitLibrary(list.data.value?.plugins ?? []));
const invalidate = [keys.plugins, keys.roles];
const upload = useAction((zip: string) => pluginActions.upload(zip), { invalidate: [keys.plugins], success: $t("插件已上传") });
const toggle = useAction(({ id, on }: { id: string; on: boolean }) => pluginActions.setEnabled(id, on), { invalidate });
const remove = useAction((id: string) => pluginActions.remove(id), { invalidate, success: $t("已删除") });
</script>

<template>
  <LibrarySection :title="$t('插件库')" :label="$t('插件库')" :help="$t('{PLUGIN_HELP} 你上传的插件只有你能看到和使用。', { PLUGIN_HELP })">
    <template #actions>
      <ZipButton :label="$t('上传插件 zip')" :pending="upload.isPending.value" @zip="(zip: string) => upload.mutate(zip)" />
    </template>
    <Loading v-if="list.isLoading.value" />
    <PluginGroup :title="$t('我的插件')" :empty="$t('还没有上传插件')" :count="groups.mine.length">
      <PluginRow v-for="p in groups.mine" :key="p.id" :plugin="p">
        <PluginActions :plugin="p" @toggle="(on: boolean) => toggle.mutate({ id: p.id, on })" @remove="remove.mutate(p.id)" />
      </PluginRow>
    </PluginGroup>
    <PluginGroup :title="$t('内置插件')" :empty="$t('管理员还没有发布内置插件')" :count="groups.builtin.length">
      <PluginRow v-for="p in groups.builtin" :key="p.id" :plugin="p" />
    </PluginGroup>
  </LibrarySection>
</template>
