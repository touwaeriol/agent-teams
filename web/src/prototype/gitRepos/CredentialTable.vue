<script setup lang="ts">
/** Prototype (docs/v2/git-repos.md §11.2): the credentials shared by repositories on the same host. */
import { CRED_KIND_LABEL, credentials } from "./fixtures";

const until = (iso?: string) => (iso ? `${Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000)} 天后到期` : "不过期");
</script>

<template>
  <div class="flex flex-col rounded-[14px] border bg-panel">
    <div v-for="c in credentials" :key="c.id" class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-sidebar px-5 py-3 text-[13px] first:border-t-0">
      <span class="flex min-w-[180px] flex-1 flex-col">
        <span class="font-medium">{{ c.name }}</span>
        <span class="text-xs text-muted-foreground">
          {{ CRED_KIND_LABEL[c.kind] }} · <span class="font-mono">{{ c.host }}</span> · 用户 <span class="font-mono">{{ c.username }}</span>
          <template v-if="c.generated"> · 平台生成</template>
        </span>
      </span>
      <span class="min-w-[90px] text-xs text-muted-foreground">{{ c.repos }} 个仓库在用</span>
      <span :class="['min-w-[90px] text-xs', c.expires_at ? 'text-warn' : 'text-muted-foreground']">{{ c.kind === "https_token" ? until(c.expires_at) : "" }}</span>
      <span class="flex gap-2 [&>.el-button]:!ml-0">
        <el-button v-if="c.public_key" size="small" text>复制公钥</el-button>
        <el-button size="small" text>替换</el-button>
        <el-button size="small" text :disabled="c.repos > 0">删除</el-button>
      </span>
    </div>
  </div>
</template>
