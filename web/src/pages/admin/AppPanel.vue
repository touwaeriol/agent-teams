<script setup lang="ts">
/** The platform App: its details and 移除 once created, else the one-click create form. */
import { ExternalLink } from "lucide-vue-next";
import { computed, ref } from "vue";
import { git } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { GitHubAppStatus } from "@/api/views";
import Card from "@/components/app/Card.vue";
import CardTitle from "@/components/app/CardTitle.vue";
import StatusPill from "@/components/app/StatusPill.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Field from "@/components/common/Field.vue";
import KeyValueList from "@/components/common/KeyValueList.vue";
import Spinner from "@/components/common/Spinner.vue";
import { postToGitHub } from "@/pages/settings/github/github";

const props = defineProps<{ status: GitHubAppStatus }>();
const organization = ref("");
const create = useAction(() => git.createApp(organization.value.trim()), { onSuccess: ({ action, manifest }) => postToGitHub(action, manifest) });
const forget = useAction(() => git.forgetApp(), { invalidate: [keys.githubApp], success: "已移除" });
const app = computed(() => (props.status.configured ? props.status.app : undefined));
const details = computed(() => {
  const a = app.value;
  if (!a) return [];
  const perms = Object.entries(a.permissions ?? {}).map(([k, v]) => `${k}: ${v}`).join("、") || "—";
  return [
    { label: "名称", value: a.name },
    { label: "所属账号", value: a.owner },
    { label: "标识", value: a.slug, mono: true },
    { label: "权限", value: perms },
    { label: "Webhook", value: a.webhook_active ? "已启用" : "未启用" },
  ];
});
</script>

<template>
  <Card v-if="app" class="gap-4">
    <CardTitle>GitHub App<template #action><StatusPill tone="#2E7D4F" label="已创建" /></template></CardTitle>
    <KeyValueList :items="details" />
    <div class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
      <el-button tag="a" :href="app.html_url" target="_blank" rel="noreferrer">在 GitHub 上查看 <ExternalLink /></el-button>
      <ConfirmAction title="移除平台的 GitHub App？" description="只从平台里移除记录，已有的 App 连接会失效。GitHub 上的 App 需要你自己去删除。" confirm-label="移除" @confirm="forget.mutate()" />
    </div>
  </Card>
  <Card v-else tone="ask" class="gap-4">
    <CardTitle>创建 GitHub App<template #action><StatusPill tone="#C2570C" label="未创建" /></template></CardTitle>
    <Field v-slot="{ id }" label="创建在哪个组织下（可选，留空则创建在你自己的 GitHub 账号下）">
      <el-input :id="id" v-model="organization" placeholder="组织名，例如 my-org" />
    </Field>
    <div>
      <el-button type="primary" :disabled="create.isPending.value" @click="create.mutate()"><Spinner v-if="create.isPending.value" /> 一键创建 GitHub App</el-button>
    </div>
    <p class="text-xs leading-5 text-muted-foreground">会跳到 GitHub 的创建确认页，点“Create GitHub App”后自动回来。权限：代码读写、PR 读写、工作流文件写入、读取 CI 检查和状态。</p>
  </Card>
</template>
