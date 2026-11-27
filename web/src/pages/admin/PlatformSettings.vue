<script setup lang="ts">
/** Platform switches: registration open, per-user node and running-thread limits, and the address
 * nodes register with (the backend itself, not a CDN in front of it). */
import { ref, watch } from "vue";
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminSettings } from "@/api/queries";
import type { AdminSettings } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import NumberSetting from "./NumberSetting.vue";

const settings = useAdminSettings();
const save = useAction((s: AdminSettings) => admin.saveSettings(s), { invalidate: [keys.adminSettings], success: "已保存" });
const put = (patch: Partial<AdminSettings>) => settings.data.value && save.mutate({ ...settings.data.value, ...patch });

const nodeURL = ref("");
watch(() => settings.data.value?.node_url, (v) => (nodeURL.value = v ?? ""), { immediate: true });
function saveNodeURL() {
  const v = nodeURL.value.trim();
  if (v !== (settings.data.value?.node_url ?? "")) put({ node_url: v });
}
</script>

<template>
  <Loading v-if="!settings.data.value" />
  <section v-else class="flex flex-wrap items-center gap-6 rounded-[14px] border bg-panel px-5 py-[18px]">
    <label class="flex items-center gap-2.5">
      <el-switch :model-value="settings.data.value.registration_open" @change="(v: string | number | boolean) => put({ registration_open: Boolean(v) })" />
      开放注册
    </label>
    <NumberSetting label="每人最多节点" :value="settings.data.value.default_max_nodes" @save="(n: number) => put({ default_max_nodes: n })" />
    <NumberSetting label="每人最多同时运行的讨论串（0 不限）" :value="settings.data.value.max_running_threads" @save="(n: number) => put({ max_running_threads: n })" />
    <div class="flex w-full flex-col gap-1.5" data-node-url="">
      <label class="flex flex-wrap items-center gap-2.5">
        节点注册地址
        <input
          v-model="nodeURL"
          type="url"
          :placeholder="settings.data.value.node_url_default"
          class="h-9 min-w-0 flex-1 rounded-lg border border-input bg-transparent px-2.5 font-mono text-[13px] sm:max-w-[360px]"
          @blur="saveNodeURL"
          @keydown.enter="saveNodeURL"
        />
      </label>
      <p class="text-xs text-muted-foreground">
        新节点的安装命令、Docker 命令和安装脚本都用这个地址连接后端。网站前面有 CDN 或转发节点时，这里填后端自己的域名。留空就用 {{ settings.data.value.node_url_default }}。已注册的节点不受影响。
      </p>
    </div>
  </section>
</template>
