// GitHub board: “连接 GitHub” installs the platform App; each connection
// lists its repositories (most recently used first) and which project uses them.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { git } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useConnections, useGitHubApp, useMe, useRepos } from "@/api/queries";
import type { GitConnectionView, RepoItem } from "@/api/views";
import { Callout, ConfirmAction, EmptyState, Field, FormDialog, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NavButton } from "@/components/shell/Shell";
import { useFlash } from "@/lib/flash";

function GitHubMark() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M12 2a10 10 0 0 0-3.2 19.5c.5.1.7-.2.7-.5v-1.7c-2.8.6-3.4-1.3-3.4-1.3-.5-1.2-1.1-1.5-1.1-1.5-.9-.6.1-.6.1-.6 1 .1 1.5 1 1.5 1 .9 1.5 2.4 1.1 2.9.8.1-.7.4-1.1.6-1.3-2.2-.3-4.6-1.1-4.6-5 0-1.1.4-2 1-2.7-.1-.3-.4-1.3.1-2.7 0 0 .8-.3 2.8 1a9.6 9.6 0 0 1 5 0c1.9-1.3 2.8-1 2.8-1 .5 1.4.2 2.4.1 2.7.6.7 1 1.6 1 2.7 0 3.9-2.4 4.7-4.6 5 .4.3.7.9.7 1.9V21c0 .3.2.6.7.5A10 10 0 0 0 12 2z" />
    </svg>
  );
}

/** Repos a project uses first, then by last push. */
export function sortRepos(list: RepoItem[]): RepoItem[] {
  const used = (r: RepoItem) => (r.used_by_projects?.length ? 1 : 0);
  return list.slice().sort((a, b) => used(b) - used(a) || Date.parse(b.pushed_at ?? "0") - Date.parse(a.pushed_at ?? "0"));
}

export function GitHubPage() {
  const qc = useQueryClient();
  const me = useMe();
  const app = useGitHubApp();
  const connections = useConnections();
  const [pasting, setPasting] = useState(false);
  useFlash({ connected: (name) => `已连接：${name}` }, () => qc.invalidateQueries({ queryKey: keys.connections }));
  const installUrl = app.data?.configured ? app.data.install_url : undefined;
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[860px] flex-col gap-5">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex items-start gap-1">
            <NavButton />
            <div className="flex flex-col gap-1.5">
              <h1 className="text-2xl font-semibold">GitHub 连接</h1>
              <p className="max-w-[600px] text-[13px] leading-[22px] text-muted-foreground">点“连接 GitHub”跳到 GitHub，选你的账号或组织、勾选仓库，授权后自动回来。成员每次开工现取 1 小时有效的令牌，只能访问你勾选的仓库。</p>
            </div>
          </div>
          <span className="flex gap-2">
            <Button variant="ghost" onClick={() => setPasting(true)}>
              粘贴令牌
            </Button>
            {installUrl && (
              <Button asChild className="h-10">
                <a href={installUrl}>
                  <GitHubMark />
                  连接 GitHub
                </a>
              </Button>
            )}
          </span>
        </header>
        {app.data && !app.data.configured && (
          <Callout tone="warn">
            平台的 GitHub App 还没创建，“连接 GitHub”暂时不可用。{me.data?.role === "admin" ? <Link to="/setup/github">去创建</Link> : "请联系管理员；也可以先粘贴一个访问令牌。"}
          </Callout>
        )}
        {connections.isLoading && <Loading />}
        {connections.data?.length === 0 && <EmptyState title="还没有连接" description="连接后，新建项目时就能直接选仓库。" />}
        {(connections.data ?? []).map((c) => (
          <ConnectionCard key={c.id} connection={c} />
        ))}
      </div>
      <PasteToken open={pasting} onOpenChange={setPasting} />
    </main>
  );
}

function ConnectionCard({ connection }: { connection: GitConnectionView }) {
  const repos = useRepos(connection.id);
  const [all, setAll] = useState(false);
  const remove = useAction(() => git.remove(connection.id), { invalidate: [keys.connections, keys.projects], success: "已断开" });
  const refresh = useAction(() => git.refresh(connection.id), { invalidate: [keys.connections], success: "已刷新权限" });
  const items = sortRepos(repos.data?.items ?? []);
  const shown = all ? items : items.slice(0, 4);
  const more = repos.data?.next_cursor ? "+" : "";
  const kind = connection.kind === "app" ? (connection.installation_settings_url?.includes("/organizations/") ? "组织" : "个人账号") : "访问令牌";
  const date = new Date(connection.created_at).toLocaleDateString("zh-CN", { month: "long", day: "numeric" });
  return (
    <article className="flex flex-col gap-3 rounded-[14px] border bg-panel px-5 py-[18px]">
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <span className="flex items-center gap-2.5">
          <span className="flex size-9 items-center justify-center rounded-full bg-raised font-semibold">{connection.login.slice(0, 1).toUpperCase()}</span>
          <span className="flex flex-col">
            <span className="font-semibold">{connection.login || connection.name}</span>
            <span className="text-xs text-muted-foreground">
              {kind} · 授权于 {date}
              {repos.data ? ` · ${items.length}${more} 个仓库` : ""}
            </span>
          </span>
        </span>
        <span className="flex gap-2">
          {connection.installation_settings_url && (
            <Button asChild variant="outline" size="sm">
              <a href={connection.installation_settings_url} target="_blank" rel="noreferrer">
                在 GitHub 上调整仓库
              </a>
            </Button>
          )}
          <ConfirmAction title={`断开 ${connection.login}？`} description="用它的项目会失去仓库凭证，直到你在项目设置里换一个连接。要彻底收回授权，请在 GitHub 上卸载 App。" confirmLabel="断开" size="sm" onConfirm={() => remove.mutate()}>
            断开
          </ConfirmAction>
        </span>
      </div>
      {connection.needs_permission_approval && (
        <Callout tone="warn">
          App 有新的权限（读取 CI 检查）等你在 GitHub 上批准，批准前平台看不到 CI 结果。{" "}
          {connection.installation_settings_url && (
            <a href={connection.installation_settings_url} target="_blank" rel="noreferrer">
              去批准
            </a>
          )}{" "}
          <button type="button" className="underline" onClick={() => refresh.mutate()}>
            我已批准，刷新
          </button>
        </Callout>
      )}
      <div className="flex flex-col rounded-[10px] border border-[#EFEDE8]">
        {repos.isLoading && <Loading className="py-4" />}
        {shown.map((r) => (
          <span key={r.full_name} className="flex items-center gap-2.5 border-t border-sidebar px-3 py-2.5 text-[13px] first:border-t-0">
            <span className="min-w-0 flex-1 truncate font-mono">{r.full_name}</span>
            <span className="text-xs text-muted-foreground">{r.default_branch}</span>
            <span className="rounded-full bg-raised px-2 py-px text-[11px] text-[#4A4843]">{r.private ? "私有" : "公开"}</span>
            <span className="min-w-[88px] text-right text-xs text-muted-foreground">{r.used_by_projects?.length ? `项目 ${r.used_by_projects.map((p) => p.name).join("、")}` : "未使用"}</span>
          </span>
        ))}
        {items.length > 4 && (
          <button type="button" onClick={() => setAll(!all)} className="border-t border-sidebar px-3 py-2 text-left text-xs text-muted-foreground hover:text-foreground">
            {all ? "收起" : `共 ${items.length}${more} 个仓库，显示最近使用的 4 个 · 显示全部`}
          </button>
        )}
        {repos.data && items.length === 0 && <span className="px-3 py-2.5 text-[13px] text-muted-foreground">这个连接看不到任何仓库。</span>}
      </div>
    </article>
  );
}

function PasteToken({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const add = useAction(() => git.addToken({ token: token.trim(), name: name.trim() || undefined, provider: "github" }), {
    invalidate: [keys.connections],
    success: (c) => `已连接：${c.login}`,
    onSuccess: () => {
      setToken("");
      setName("");
      onOpenChange(false);
    },
  });
  return (
    <FormDialog open={open} onOpenChange={onOpenChange} title="粘贴访问令牌" description="GitHub 个人访问令牌（fine-grained 或 classic，需要仓库读写权限）。加密保存，只在运行时下发给员工。" submitLabel="连接" busy={add.isPending} disabled={!token.trim()} onSubmit={() => add.mutate()}>
      <Field label="令牌">
        <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoFocus className="font-mono" placeholder="github_pat_…" />
      </Field>
      <Field label="名称（可选）">
        <Input value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
