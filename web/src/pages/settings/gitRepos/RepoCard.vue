<script setup lang="ts">
/** One standalone repository, styled like ServerCard: URL, credential, access, default branch, host key,
 * projects; 测试连接 / 编辑 / 重置主机密钥 / 删除. */
import { GitBranch, KeyRound, Lock } from "lucide-vue-next";
import { computed, ref } from "vue";
import { gitRepos } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { GitCredentialView, GitRepoView, GitTestResult } from "@/api/views";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Spinner from "@/components/common/Spinner.vue";
import { accessText, CRED_KIND_LABEL, daysLeft, PROVIDER_LABEL, repoTitle } from "@/lib/gitRepos";
import { timeAgo } from "@/lib/utils";
import TestResult from "./TestResult.vue";

const props = defineProps<{ repo: GitRepoView; credentials: GitCredentialView[] }>();
const emit = defineEmits<{ edit: [] }>();
const cred = computed(() => props.credentials.find((c) => c.id === props.repo.credential_id) ?? null);
const title = computed(() => repoTitle(props.repo));
const access = computed(() => accessText(props.repo));
const days = computed(() => daysLeft(cred.value?.expires_at));
const result = ref<GitTestResult | null>(null);
const test = useAction(() => gitRepos.test(props.repo.id), { invalidate: [keys.gitRepos], onSuccess: (r) => (result.value = r) });
const reset = useAction(() => gitRepos.resetHostKey(props.repo.id), {
  invalidate: [keys.gitRepos],
  success: "主机密钥已重置，再测试一次会重新固定",
  onSuccess: () => (result.value = null),
});
const remove = useAction(() => gitRepos.remove(props.repo.id), { invalidate: [keys.gitRepos, keys.gitCredentials], success: "仓库已删除" });
const TONE = { ok: "text-ok", warn: "text-warn", bad: "text-bad" } as const;
</script>

<template>
  <article class="flex flex-col gap-3 rounded-[14px] border bg-panel px-5 py-[18px]" :data-git-repo="repo.slug">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex min-w-0 flex-wrap items-center gap-2.5">
        <span class="font-mono text-[17px] font-semibold">{{ title }}</span>
        <span class="rounded-full bg-raised px-2 py-px text-[11px] text-muted-foreground">{{ PROVIDER_LABEL[repo.provider] }}</span>
        <span class="min-w-0 break-all font-mono text-[13px] text-muted-foreground">{{ repo.url }}</span>
      </span>
      <span class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
        <el-button size="small" :disabled="test.isPending.value" @click="test.mutate()"><Spinner v-if="test.isPending.value" /> 测试连接</el-button>
        <el-button size="small" @click="emit('edit')">编辑</el-button>
        <ConfirmAction
          v-if="repo.host_key_fp"
          :title="`重置 ${title} 的主机密钥？`"
          description="只在服务器确实换了主机密钥时使用。重置后下一次测试连接会重新固定看到的密钥。"
          confirm-label="重置"
          variant="primary"
          size="small"
          @confirm="reset.mutate()"
        >重置主机密钥</ConfirmAction>
        <ConfirmAction
          :title="`删除仓库 ${title}？`"
          :description="repo.projects.length ? `还有项目在用：${repo.projects.map((p) => p.name).join('、')}。先在项目设置里移除。` : '平台上的镜像会一并删除；上游仓库不受影响。'"
          size="small"
          @confirm="remove.mutate()"
        >删除</ConfirmAction>
      </span>
    </div>
    <dl class="grid grid-cols-1 gap-x-6 gap-y-1.5 text-[13px] sm:grid-cols-2">
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">认证方式</dt>
        <dd class="flex min-w-0 items-center gap-1.5">
          <template v-if="cred">
            <KeyRound v-if="cred.kind === 'ssh_key'" class="size-3.5 shrink-0" /><Lock v-else class="size-3.5 shrink-0" />
            <span class="truncate">{{ CRED_KIND_LABEL[cred.kind] }} · {{ cred.name }}</span>
          </template>
          <template v-else>公开仓库（只读）</template>
        </dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">访问权限</dt>
        <dd class="min-w-0">
          <span :class="TONE[access.tone]">{{ access.text }}</span>
          <span v-if="repo.last_checked_at" class="text-muted-foreground"> · {{ timeAgo(repo.last_checked_at) }}检查</span>
        </dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">默认分支</dt>
        <dd class="flex items-center gap-1.5 font-mono text-xs"><GitBranch class="size-3.5" />{{ repo.default_branch || "—" }}</dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">凭据更新</dt>
        <dd>
          {{ cred ? timeAgo(cred.cred_updated_at) : "—" }}
          <span v-if="days !== null && days <= 14" class="text-warn"> · {{ days <= 0 ? "令牌已过期" : `令牌 ${days} 天后到期` }}</span>
        </dd>
      </div>
      <div v-if="repo.transport === 'ssh'" class="flex min-w-0 gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">主机指纹</dt>
        <dd class="min-w-0">
          <span v-if="repo.host_key_fp" class="break-all font-mono text-xs leading-5">{{ repo.host_key_fp }}</span>
          <span v-else class="text-warn">还没固定：点“测试连接”，第一次连接时固定</span>
        </dd>
      </div>
      <div class="flex gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">使用的项目</dt>
        <dd class="text-foreground/85">{{ repo.projects.length ? repo.projects.map((p) => p.name).join("、") : "还没有项目使用（在项目设置 → 仓库里添加）" }}</dd>
      </div>
      <div class="flex gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">能力</dt>
        <dd class="text-muted-foreground">节点直连仓库，凭据只给员工的 git 用 · 没有 PR 和 CI：讨论串的改动留在讨论串分支上，完成即交付</dd>
      </div>
    </dl>
    <TestResult v-if="result" :result="result" />
  </article>
</template>
