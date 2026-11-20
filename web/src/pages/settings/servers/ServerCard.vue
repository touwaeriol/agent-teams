<script setup lang="ts">
/** One server: name, user@host:port, auth, pinned host key, credential age, projects; 测试连接 / 编辑 / 访问记录 / 删除. */
import { KeyRound, Lock } from "lucide-vue-next";
import { ref } from "vue";
import { servers as serverActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ServerTestResult, ServerView } from "@/api/views";
import ServerTestPanel from "@/components/app/servers/ServerTestPanel.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Spinner from "@/components/common/Spinner.vue";
import { AUTH_LABEL, endpoint } from "@/lib/servers";
import { timeAgo } from "@/lib/utils";
import ServerAudit from "./ServerAudit.vue";

const props = defineProps<{ server: ServerView }>();
const emit = defineEmits<{ edit: [] }>();
const result = ref<ServerTestResult | null>(null);
const showAudit = ref(false);
const test = useAction(() => serverActions.test(props.server.id), {
  invalidate: () => [keys.servers, keys.serverAudit(props.server.id)],
  onSuccess: (r) => (result.value = r),
});
const reset = useAction(() => serverActions.resetHostKey(props.server.id), {
  invalidate: [keys.servers],
  success: "主机密钥已重置，再测试一次会重新固定",
  onSuccess: () => (result.value = null),
});
const remove = useAction(() => serverActions.remove(props.server.id), { invalidate: [keys.servers], success: "服务器已删除" });
</script>

<template>
  <article class="flex flex-col gap-3 rounded-[14px] border bg-panel px-5 py-[18px]" :data-server="server.name">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex min-w-0 flex-wrap items-center gap-2.5">
        <span class="font-mono text-[17px] font-semibold">{{ server.name }}</span>
        <span class="font-mono text-[13px] text-muted-foreground">{{ endpoint(server) }}</span>
      </span>
      <span class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
        <el-button size="small" :disabled="test.isPending.value" @click="test.mutate()"><Spinner v-if="test.isPending.value" /> 测试连接</el-button>
        <el-button size="small" @click="emit('edit')">编辑</el-button>
        <el-button size="small" @click="showAudit = !showAudit">{{ showAudit ? "收起记录" : "访问记录" }}</el-button>
        <ConfirmAction
          :title="`删除服务器 ${server.name}？`"
          description="会从所有项目里移除，员工的会话立即连不上它；访问记录保留。"
          size="small"
          @confirm="remove.mutate()"
        >删除</ConfirmAction>
      </span>
    </div>
    <dl class="grid grid-cols-1 gap-x-6 gap-y-1.5 text-[13px] sm:grid-cols-2">
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">认证方式</dt>
        <dd class="flex items-center gap-1.5">
          <KeyRound v-if="server.auth_kind === 'private_key'" class="size-3.5" /><Lock v-else class="size-3.5" />
          {{ AUTH_LABEL[server.auth_kind] }}<template v-if="server.has_passphrase">（有口令）</template>
        </dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">凭证更新</dt>
        <dd>{{ timeAgo(server.cred_updated_at) }}</dd>
      </div>
      <div v-if="server.key_fingerprint" class="flex min-w-0 gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">私钥指纹</dt>
        <dd class="min-w-0 break-all font-mono text-xs leading-5">{{ server.key_fingerprint }}</dd>
      </div>
      <div class="flex min-w-0 gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">主机指纹</dt>
        <dd class="min-w-0">
          <span v-if="server.host_key_fp" class="break-all font-mono text-xs leading-5">{{ server.host_key_fp }}</span>
          <span v-else class="text-warn">还没固定：点“测试连接”，第一次连接时固定</span>
        </dd>
      </div>
      <div class="flex gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">使用的项目</dt>
        <dd class="text-foreground/85">{{ server.projects?.length ? server.projects.map((p) => p.name).join("、") : "还没有项目使用（在项目设置 → 环境里选用）" }}</dd>
      </div>
    </dl>
    <ServerTestPanel v-if="result" :result="result" :resetting="reset.isPending.value" @reset="reset.mutate()" />
    <ServerAudit v-if="showAudit" :server-id="server.id" />
  </article>
</template>
