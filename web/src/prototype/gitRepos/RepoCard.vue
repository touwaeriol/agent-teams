<script setup lang="ts">
/** Prototype (docs/v2/git-repos.md §11.2): one standalone repository, styled like ServerCard. */
import { GitBranch, KeyRound, Lock } from "lucide-vue-next";
import { computed } from "vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import { timeAgo } from "@/lib/utils";
import { CRED_KIND_LABEL, credentialOf, PROVIDER_LABEL, type ProtoRepo } from "./fixtures";

const props = defineProps<{ repo: ProtoRepo }>();
const emit = defineEmits<{ edit: [] }>();
const cred = computed(() => credentialOf(props.repo));
const title = computed(() => props.repo.name || props.repo.slug.split("/").pop());
const expiresSoon = computed(() => !!cred.value?.expires_at && new Date(cred.value.expires_at).getTime() - Date.now() < 14 * 86_400_000);
const days = computed(() => (cred.value?.expires_at ? Math.ceil((new Date(cred.value.expires_at).getTime() - Date.now()) / 86_400_000) : 0));
</script>

<template>
  <article class="flex flex-col gap-3 rounded-[14px] border bg-panel px-5 py-[18px]">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex min-w-0 flex-wrap items-center gap-2.5">
        <span class="font-mono text-[17px] font-semibold">{{ title }}</span>
        <span class="rounded-full bg-raised px-2 py-px text-[11px] text-[#4A4843]">{{ PROVIDER_LABEL[repo.provider] }}</span>
        <span class="min-w-0 break-all font-mono text-[13px] text-muted-foreground">{{ repo.url }}</span>
      </span>
      <span class="flex flex-wrap gap-2 [&>.el-button]:!ml-0">
        <el-button size="small">测试连接</el-button>
        <el-button size="small" @click="emit('edit')">编辑</el-button>
        <ConfirmAction
          :title="`删除仓库 ${title}？`"
          :description="repo.projects.length ? `还有项目在用：${repo.projects.join('、')}。先在项目设置里移除。` : '平台上的镜像会一并删除；上游仓库不受影响。'"
          size="small"
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
        <dd>
          <span v-if="repo.access === 'write'" class="text-ok">可读写</span>
          <span v-else-if="repo.access === 'read'" class="text-warn">只读：员工推送会失败，请给凭据写权限</span>
          <span v-else class="text-warn">还没测试</span>
          <span class="text-muted-foreground"> · {{ timeAgo(repo.last_checked_at) }}检查</span>
        </dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">默认分支</dt>
        <dd class="flex items-center gap-1.5 font-mono text-xs"><GitBranch class="size-3.5" />{{ repo.default_branch }}</dd>
      </div>
      <div class="flex gap-2">
        <dt class="w-20 shrink-0 text-muted-foreground">凭据更新</dt>
        <dd>
          {{ cred ? timeAgo(cred.cred_updated_at) : "—" }}
          <span v-if="expiresSoon" class="text-warn"> · 令牌 {{ days }} 天后到期</span>
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
        <dd class="text-foreground/85">{{ repo.projects.length ? repo.projects.join("、") : "还没有项目使用（在项目设置 → 仓库里添加）" }}</dd>
      </div>
      <div class="flex gap-2 sm:col-span-2">
        <dt class="w-20 shrink-0 text-muted-foreground">能力</dt>
        <dd class="text-muted-foreground">Git 读写经平台中转 · 没有 PR 和 CI：讨论串完成后推送分支，或在平台里验收后由平台合并</dd>
      </div>
    </dl>
  </article>
</template>
