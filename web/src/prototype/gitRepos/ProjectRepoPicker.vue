<script setup lang="ts">
/** Prototype (docs/v2/git-repos.md §7, §8.2, §11.2), dev only at /settings/git-prototype/picker:
 * 项目设置 → 仓库 with GitHub and standalone repositories mixed, the grouped picker (drawn open),
 * and the acceptance card of a standalone repository (no PR: commits and diff shown in the platform). */
import { ArrowDown, ArrowUp, GitBranch, GitCommitHorizontal, Lock, Plus, Search, X } from "lucide-vue-next";
import NavButton from "@/components/shell/NavButton.vue";

const BTN = "flex size-8 shrink-0 items-center justify-center rounded-lg hover:bg-raised";
const rows = [
  { repo: "touwaeriol/agent-teams", source: "GitHub · touwaeriol", tag: "" },
  { repo: "gitlab.com/prophey/sup2api", source: "GitLab · GitLab 个人令牌", tag: "推送分支交付" },
  { repo: "git.prophey.ai:2222/infra/deploy", source: "自建 Git · infra 部署密钥", tag: "推送分支交付" },
];
const github = [
  { repo: "touwaeriol/new-api", note: "已被 new-api 使用", priv: false },
  { repo: "touwaeriol/claude-relay-service", note: "", priv: false },
  { repo: "touwaeriol/repo-1", note: "", priv: true },
];
const standalone = [{ repo: "gitlab.com/prophey/docs-site", note: "GitLab · 只读" }];
const files = [
  { path: "internal/relay/stream.go", add: 84, del: 12 },
  { path: "internal/relay/stream_test.go", add: 131, del: 0 },
  { path: "config/example.yaml", add: 3, del: 1 },
];
</script>

<template>
  <main class="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
    <div class="mx-auto flex max-w-[720px] flex-col gap-6">
      <header class="flex items-start gap-1">
        <NavButton />
        <div class="flex flex-col gap-1.5">
          <h1 class="text-2xl font-semibold">项目设置 → 仓库</h1>
          <p class="text-[13px] leading-[22px] text-muted-foreground">原型：GitHub 仓库和单独添加的仓库混在一个项目里，下拉框分组。</p>
        </div>
      </header>

      <section class="flex flex-col rounded-xl border bg-panel" aria-label="每个讨论串中都包含的仓库">
        <div class="flex min-h-12 items-center justify-between gap-3 px-4">
          <span class="text-[13px] text-muted-foreground">每个讨论串中都包含的仓库</span>
          <el-button size="small" type="primary">保存</el-button>
        </div>
        <ol class="flex flex-col divide-y border-t">
          <li v-for="(r, i) in rows" :key="r.repo" class="flex items-center gap-2 px-3 py-2">
            <span class="w-5 text-[12px] tabular-nums text-muted-foreground">{{ i + 1 }}</span>
            <span class="min-w-0 flex-1 truncate font-mono text-[13px]">{{ r.repo }}</span>
            <span v-if="r.tag" class="rounded-full bg-warn/10 px-2 py-px text-[11px] text-warn" title="没有 PR 和 CI：讨论串完成后推送分支，或在平台里验收后由平台合并">{{ r.tag }}</span>
            <span class="max-w-[34%] truncate text-[12px] text-muted-foreground max-sm:hidden">{{ r.source }}</span>
            <button type="button" :class="BTN" :aria-label="`上移 ${r.repo}`"><ArrowUp class="size-4" /></button>
            <button type="button" :class="BTN" :aria-label="`下移 ${r.repo}`"><ArrowDown class="size-4" /></button>
            <button type="button" :class="BTN" :aria-label="`移除 ${r.repo}`"><X class="size-4" /></button>
          </li>
        </ol>
        <div class="flex flex-col gap-1.5 border-t px-4 py-3">
          <span class="text-[13px] font-medium text-foreground/85">再加一个仓库</span>
          <div class="flex h-9 items-center gap-2 rounded-lg border border-primary px-3 text-[13px] text-muted-foreground"><Search class="size-4" />搜索仓库，或粘贴地址</div>
          <div class="flex flex-col overflow-hidden rounded-lg border bg-panel shadow-[0_8px_24px_rgba(0,0,0,0.08)]">
            <span class="bg-well/60 px-3 py-1.5 text-[11px] text-muted-foreground">GitHub · touwaeriol</span>
            <span v-for="g in github" :key="g.repo" class="flex items-center gap-2 px-3 py-2 text-[13px] hover:bg-raised">
              <span class="min-w-0 flex-1 truncate font-mono">{{ g.repo }}</span>
              <span v-if="g.note" class="text-xs text-muted-foreground">{{ g.note }}</span>
              <Lock v-else-if="g.priv" class="size-3.5 text-muted-foreground" />
            </span>
            <span class="border-t bg-well/60 px-3 py-1.5 text-[11px] text-muted-foreground">单独添加的仓库</span>
            <span v-for="s in standalone" :key="s.repo" class="flex items-center gap-2 px-3 py-2 text-[13px] hover:bg-raised">
              <span class="min-w-0 flex-1 truncate font-mono">{{ s.repo }}</span>
              <span class="text-xs text-muted-foreground">{{ s.note }}</span>
            </span>
            <span class="flex items-center gap-2 border-t px-3 py-2 text-[13px] text-primary"><Plus class="size-4" />添加新的 Git 仓库（GitLab、Gitea、自建）…</span>
          </div>
        </div>
        <p class="border-t px-4 py-2.5 text-[12px] leading-5 text-muted-foreground">
          基准分支、分支约定等写在项目指令里，统筹和组长会按它开分支。标着“推送分支交付”的仓库没有 PR 和 CI：讨论串完成后平台推送讨论串分支，或按“讨论串完成后”的设置由平台合并。
        </p>
      </section>

      <section class="flex flex-col gap-3 rounded-xl border bg-panel px-5 py-4" aria-label="验收卡片">
        <span class="text-xs text-muted-foreground">讨论串 #14 · 等你验收（单独添加的仓库，没有 PR）</span>
        <span class="text-[15px] font-semibold">流式转发支持断线续传</span>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px]">
          <span class="font-mono">gitlab.com/prophey/sup2api</span>
          <span class="flex items-center gap-1 font-mono text-xs text-muted-foreground"><GitBranch class="size-3.5" />thread/14-stream-resume → develop</span>
          <span class="flex items-center gap-1 text-xs text-muted-foreground"><GitCommitHorizontal class="size-3.5" />5 个提交</span>
          <span class="text-xs"><span class="text-ok">+218</span> <span class="text-bad">−13</span></span>
        </div>
        <div class="flex flex-col rounded-[10px] border">
          <span v-for="f in files" :key="f.path" class="flex items-center gap-2.5 border-t border-sidebar px-3 py-2 text-[13px] first:border-t-0">
            <span class="min-w-0 flex-1 truncate font-mono">{{ f.path }}</span>
            <span class="text-xs"><span class="text-ok">+{{ f.add }}</span> <span class="text-bad">−{{ f.del }}</span></span>
          </span>
          <button type="button" class="border-t border-sidebar px-3 py-2 text-left text-xs text-muted-foreground hover:text-foreground">查看完整改动</button>
        </div>
        <p class="text-xs text-muted-foreground">验收后平台把讨论串分支 squash 合并进 develop 并推送到 GitLab。也可以 <a class="underline" href="#">去 GitLab 创建合并请求</a> 自己合并，平台会发现并收尾。</p>
        <span class="flex gap-2 [&>.el-button]:!ml-0">
          <el-button type="primary">验收并合并</el-button>
          <el-button>退回修改</el-button>
        </span>
      </section>
    </div>
  </main>
</template>
