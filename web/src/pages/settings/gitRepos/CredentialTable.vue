<script setup lang="ts">
/** The credentials shared by repositories on the same host: copy a generated public key, replace, delete. */
import { gitCredentials } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { GitCredentialView } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import { CRED_KIND_LABEL, daysLeft, expiryText } from "@/lib/gitRepos";
import { toast } from "@/lib/toast";

defineProps<{ credentials: GitCredentialView[] }>();
const emit = defineEmits<{ replace: [credential: GitCredentialView] }>();
const remove = useAction((id: string) => gitCredentials.remove(id), { invalidate: [keys.gitCredentials], success: "凭据已删除" });
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success("公钥已复制");
  } catch {
    toast.error("复制失败，请手动选中复制");
  }
}
const soon = (c: GitCredentialView) => {
  const d = daysLeft(c.expires_at);
  return d !== null && d <= 14;
};
</script>

<template>
  <div class="flex flex-col rounded-[14px] border bg-panel">
    <div v-for="c in credentials" :key="c.id" class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-sidebar px-5 py-3 text-[13px] first:border-t-0" :data-git-credential="c.name">
      <span class="flex min-w-[180px] flex-1 flex-col">
        <span class="font-medium">{{ c.name }}</span>
        <span class="text-xs text-muted-foreground">
          {{ CRED_KIND_LABEL[c.kind] }} · <span class="font-mono">{{ c.host }}</span> · 用户 <span class="font-mono">{{ c.username }}</span>
          <template v-if="c.generated"> · 平台生成</template>
          <template v-if="c.key_fingerprint"> · <span class="font-mono">{{ c.key_fingerprint }}</span></template>
        </span>
      </span>
      <span class="min-w-[90px] text-xs text-muted-foreground">{{ c.repos }} 个仓库在用</span>
      <span :class="['min-w-[90px] text-xs', soon(c) ? 'text-warn' : 'text-muted-foreground']">{{ expiryText(c) }}</span>
      <span class="flex gap-2 [&>.el-button]:!ml-0">
        <el-button v-if="c.public_key" size="small" text @click="copy(c.public_key)">复制公钥</el-button>
        <el-button size="small" text @click="emit('replace', c)">替换</el-button>
        <ConfirmAction
          :title="`删除凭据 ${c.name}？`"
          :description="c.repos > 0 ? '还有仓库在用它：先给那些仓库换一条凭据。' : '删除后无法恢复，上游的令牌或部署密钥请到对应平台吊销。'"
          size="small"
          @confirm="remove.mutate(c.id)"
        >删除</ConfirmAction>
      </span>
    </div>
  </div>
</template>
