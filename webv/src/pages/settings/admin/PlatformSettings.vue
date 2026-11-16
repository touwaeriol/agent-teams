<script setup lang="ts">
/** Platform switches: registration open, per-user node and running-thread limits. */
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminSettings } from "@/api/queries";
import type { AdminSettings } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import NumberSetting from "./NumberSetting.vue";

const settings = useAdminSettings();
const save = useAction((s: AdminSettings) => admin.saveSettings(s), { invalidate: [keys.adminSettings], success: "已保存" });
const put = (patch: Partial<AdminSettings>) => settings.data.value && save.mutate({ ...settings.data.value, ...patch });
</script>

<template>
  <Loading v-if="!settings.data.value" />
  <section v-else class="flex flex-wrap items-center gap-6 rounded-[14px] border bg-panel px-5 py-[18px]">
    <label class="flex items-center gap-2.5">
      <el-switch :model-value="settings.data.value.registration_open" @change="(v: string | number | boolean) => put({ registration_open: Boolean(v) })" />
      开放注册
    </label>
    <NumberSetting label="每人最多节点" :value="settings.data.value.default_max_nodes" @save="(n: number) => put({ default_max_nodes: n })" />
    <NumberSetting label="每人最多同时运行的讨论串" :value="settings.data.value.max_running_threads" @save="(n: number) => put({ max_running_threads: n })" />
  </section>
</template>
