<script setup lang="ts">
/** The outcome of 测试连接: access, default branch, branches, and an SSH host key when one was seen. */
import { CircleAlert, CircleCheck } from "lucide-vue-next";
import type { GitTestResult } from "@/api/views";
import { testSummary } from "@/lib/gitRepos";

defineProps<{ result: GitTestResult }>();
</script>

<template>
  <div
    :class="['flex flex-col gap-1.5 rounded-xl border px-4 py-3 text-[13px]', result.ok ? 'border-ok/40 bg-ok/5' : 'border-bad/40 bg-bad/5']"
    role="status"
    data-git-test
  >
    <span :class="['flex items-center gap-2 font-medium', result.ok ? 'text-ok' : 'text-bad']">
      <CircleCheck v-if="result.ok" class="size-4 shrink-0" /><CircleAlert v-else class="size-4 shrink-0" />{{ testSummary(result) }}
    </span>
    <template v-if="result.host_key_fp">
      <span class="text-muted-foreground">主机密钥{{ result.host_key_pinned ? "（已固定）" : "（保存后第一次测试时固定）" }}</span>
      <span class="break-all font-mono text-xs">{{ result.host_key_type }} {{ result.host_key_fp }}</span>
      <span class="text-xs text-muted-foreground">在服务器上核对：ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</span>
    </template>
  </div>
</template>
