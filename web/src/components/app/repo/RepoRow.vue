<script setup lang="ts">
import { $t } from "@/i18n";
import { ArrowDown, ArrowUp, X } from "lucide-vue-next";
import { computed } from "vue";
import { useGitRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import BranchSelect from "../session/BranchSelect.vue";
import { sourceLabel, type RepoRef } from "./repo";

const props = defineProps<{ item: RepoRef; index: number; count: number; connections: GitConnectionView[] }>();
const emit = defineEmits<{ move: [delta: number]; remove: []; base: [branch: string] }>();
const base = computed({ get: () => props.item.base_branch ?? "", set: (v: string) => emit("base", v) });
const gitRepos = useGitRepos();
const source = computed(() => sourceLabel(props.item, props.connections, gitRepos.data.value ?? []));
const BTN = "flex size-8 shrink-0 items-center justify-center rounded-lg hover:bg-raised disabled:pointer-events-none disabled:opacity-50";
</script>

<template>
  <li class="flex items-center gap-2 px-3 py-2">
    <span class="w-5 text-[12px] tabular-nums text-muted-foreground">{{ index + 1 }}</span>
    <span class="min-w-0 flex-1 truncate font-mono text-[13px]" :title="item.repo">{{ item.repo }}</span>
    <span
      v-if="item.git_repo_id"
      class="shrink-0 rounded-full bg-warn/10 px-2 py-px text-[11px] text-warn"
      :title="$t('没有 PR 和 CI：讨论串的改动留在讨论串分支上，完成即交付')"
    >{{ $t("推送分支交付") }}</span>
    <span class="max-w-[34%] truncate text-[12px] text-muted-foreground max-sm:hidden" :title="source">{{ source }}</span>
    <BranchSelect v-model="base" :item="item" :clearable="false" :label="$t('{repo} 的起点分支', { repo: item.repo })" :title="$t('起点分支：讨论串从这里开始，PR 合回这里')" />
    <button type="button" :class="BTN" :aria-label="$t('上移 {repo}', { repo: item.repo })" :disabled="index === 0" @click="emit('move', -1)"><ArrowUp class="size-4" /></button>
    <button type="button" :class="BTN" :aria-label="$t('下移 {repo}', { repo: item.repo })" :disabled="index === count - 1" @click="emit('move', 1)"><ArrowDown class="size-4" /></button>
    <button type="button" :class="BTN" :aria-label="$t('移除 {repo}', { repo: item.repo })" @click="emit('remove')"><X class="size-4" /></button>
  </li>
</template>
