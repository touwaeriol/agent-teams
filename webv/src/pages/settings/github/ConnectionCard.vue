<script setup lang="ts">
// One GitHub connection: who and when, its repositories (most recently used first, four until
// expanded) and which project uses each; disconnect, and the CI-permission approval notice.
import { computed, ref } from "vue";
import { git } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import { sortRepos } from "@/components/app/repo/repo";
import Callout from "@/components/common/Callout.vue";
import ConfirmAction from "@/components/common/ConfirmAction.vue";
import Loading from "@/components/common/Loading.vue";
import { connectionKind, REPOS_SHOWN, shortDate, usedText } from "./github";

const props = defineProps<{ connection: GitConnectionView }>();
const repos = useRepos(() => props.connection.id);
const all = ref(false);
const remove = useAction(() => git.remove(props.connection.id), { invalidate: [keys.connections, keys.projects], success: "已断开" });
const refresh = useAction(() => git.refresh(props.connection.id), { invalidate: [keys.connections], success: "已刷新权限" });
const items = computed(() => sortRepos(repos.data.value?.items ?? []));
const shown = computed(() => (all.value ? items.value : items.value.slice(0, REPOS_SHOWN)));
const more = computed(() => (repos.data.value?.next_cursor ? "+" : ""));
const settingsUrl = computed(() => props.connection.installation_settings_url ?? "");
</script>

<template>
  <article class="flex flex-col gap-3 rounded-[14px] border bg-panel px-5 py-[18px]">
    <div class="flex flex-wrap items-center justify-between gap-2.5">
      <span class="flex items-center gap-2.5">
        <span class="flex size-9 items-center justify-center rounded-full bg-raised font-semibold">{{ connection.login.slice(0, 1).toUpperCase() }}</span>
        <span class="flex flex-col">
          <span class="font-semibold">{{ connection.login || connection.name }}</span>
          <span class="text-xs text-muted-foreground">
            {{ connectionKind(connection) }} · 授权于 {{ shortDate(connection.created_at) }}{{ repos.data.value ? ` · ${items.length}${more} 个仓库` : "" }}
          </span>
        </span>
      </span>
      <span class="flex gap-2 [&>.el-button]:!ml-0">
        <el-button v-if="settingsUrl" tag="a" :href="settingsUrl" target="_blank" rel="noreferrer" size="small">在 GitHub 上调整仓库</el-button>
        <ConfirmAction
          :title="`断开 ${connection.login}？`"
          description="用它的项目会失去仓库凭证，直到你在项目设置里换一个连接。要彻底收回授权，请在 GitHub 上卸载 App。"
          confirm-label="断开"
          size="small"
          @confirm="remove.mutate()"
        >
          断开
        </ConfirmAction>
      </span>
    </div>
    <Callout v-if="connection.needs_permission_approval" tone="warn">
      App 有新的权限（读取 CI 检查）等你在 GitHub 上批准，批准前平台看不到 CI 结果。
      <a v-if="settingsUrl" :href="settingsUrl" target="_blank" rel="noreferrer">去批准</a>
      <button type="button" class="underline" @click="refresh.mutate()">我已批准，刷新</button>
    </Callout>
    <div class="flex flex-col rounded-[10px] border border-[#EFEDE8]">
      <Loading v-if="repos.isLoading.value" class="py-4" />
      <span v-for="r in shown" :key="r.full_name" class="flex items-center gap-2.5 border-t border-sidebar px-3 py-2.5 text-[13px] first:border-t-0">
        <span class="min-w-0 flex-1 truncate font-mono">{{ r.full_name }}</span>
        <span class="text-xs text-muted-foreground">{{ r.default_branch }}</span>
        <span class="rounded-full bg-raised px-2 py-px text-[11px] text-[#4A4843]">{{ r.private ? "私有" : "公开" }}</span>
        <span class="min-w-[88px] text-right text-xs text-muted-foreground">{{ usedText(r) }}</span>
      </span>
      <button v-if="items.length > REPOS_SHOWN" type="button" class="border-t border-sidebar px-3 py-2 text-left text-xs text-muted-foreground hover:text-foreground" @click="all = !all">
        {{ all ? "收起" : `共 ${items.length}${more} 个仓库，显示最近使用的 ${REPOS_SHOWN} 个 · 显示全部` }}
      </button>
      <span v-if="repos.data.value && items.length === 0" class="px-3 py-2.5 text-[13px] text-muted-foreground">这个连接看不到任何仓库。</span>
    </div>
  </article>
</template>
