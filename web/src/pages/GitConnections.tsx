import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Github, Lock, Plus } from "lucide-react";
import { useState } from "react";
import { ConfirmAction, EmptyState, errorText, Field, FormDialog, KeyValueList, Loading, Page, Panel, PickList, StatusBadge, type Tone } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, type GitConnection, type Repo } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { keys, useConnections } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

const TOKEN_URL =
  "https://github.com/settings/personal-access-tokens/new?name=agent-teams&description=Agent%20Teams&contents=write&pull_requests=write&metadata=read";

const METHODS: { title: string; status: string; tone: Tone; text: string }[] = [
  { title: "GitHub 访问令牌", status: "可用", tone: "ok", text: "粘贴一个 fine-grained personal access token，勾选需要的仓库，权限给 Contents 和 Pull requests 读写。配置最快。" },
  { title: "GitHub App", status: "计划中", tone: "neutral", text: "平台一键创建 App，安装到组织并选择仓库；按员工身份区分读写，令牌 1 小时过期。" },
  { title: "GitLab · 其他平台 · SSH", status: "以后", tone: "neutral", text: "暂不支持。" },
];

export function GitConnectionsPage() {
  const connections = useConnections();
  const [adding, setAdding] = useState(false);
  return (
    <Page
      title="Git 连接"
      description="在这里授权一次，所有项目和节点共用。令牌加密保存在服务端，不写进节点的文件；员工运行时 git 和 gh 通过环境变量拿到它，可以拉取、推送分支和建 PR。第一版只支持 GitHub。"
      actions={
        <Button onClick={() => setAdding(true)}>
          <Plus /> 添加 GitHub 连接
        </Button>
      }
    >
      {connections.isLoading && <Loading />}
      {connections.data?.length === 0 && (
        <EmptyState
          icon={<Github />}
          title="还没有 Git 连接"
          description="添加一个 GitHub 访问令牌后，新建项目时就能直接选择仓库。"
          action={<Button onClick={() => setAdding(true)}>添加 GitHub 连接</Button>}
        />
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        {connections.data?.map((c) => <ConnectionCard key={c.id} connection={c} />)}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        {METHODS.map((m) => (
          <Panel key={m.title} padding="sm" title={m.title} action={<StatusBadge tone={m.tone} label={m.status} />}>
            <p className="text-xs leading-5 text-muted-foreground">{m.text}</p>
          </Panel>
        ))}
      </div>
      <AddConnectionDialog open={adding} onOpenChange={setAdding} />
    </Page>
  );
}

function ConnectionCard({ connection }: { connection: GitConnection }) {
  const [showRepos, setShowRepos] = useState(false);
  const repos = useQuery({
    queryKey: keys.repos(connection.id),
    queryFn: () => api<Repo[]>(`/api/git/connections/${connection.id}/repos`),
    enabled: showRepos,
  });
  const remove = useAction(() => api(`/api/git/connections/${connection.id}`, { method: "DELETE" }), {
    invalidate: [keys.connections, keys.projects],
    success: "连接已删除",
  });
  return (
    <Panel as="article" title={connection.name} icon={<Github />} action={<StatusBadge tone="ok" label="正常" />} description="访问令牌 · 加密保存，经运行环境下发给员工，不落盘">
      <KeyValueList
        items={[
          { label: "GitHub 账号", value: connection.login },
          { label: "添加时间", value: timeAgo(connection.created_at) },
        ]}
      />
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => setShowRepos(!showRepos)}>
          {showRepos ? "收起仓库" : "查看可访问的仓库"}
        </Button>
        <ConfirmAction
          title={`删除连接「${connection.name}」？`}
          description="用它的项目会失去仓库凭证，员工将无法再拉取或推送私有仓库，直到你在项目设置里换一个连接。"
          onConfirm={() => remove.mutate()}
        />
      </div>
      {showRepos && (
        <PickList
          className="max-h-64"
          items={repos.data ?? []}
          itemKey={(r) => r.full_name}
          loading={repos.isLoading}
          error={repos.error ? errorText(repos.error) : undefined}
          empty="这个令牌看不到任何仓库"
          onPick={(r) => window.open(r.html_url, "_blank", "noreferrer")}
          render={(r) => (
            <>
              <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{r.full_name}</span>
              {r.private && <Lock className="size-3.5 text-muted-foreground" aria-label="私有" />}
              <span className="text-[11px] text-muted-foreground">{r.default_branch}</span>
              <ExternalLink className="size-3 text-muted-foreground" />
            </>
          )}
        />
      )}
    </Panel>
  );
}

function AddConnectionDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const add = useAction(() => api<GitConnection>("/api/git/connections", { method: "POST", json: { token: token.trim(), name: name.trim() } }), {
    invalidate: [keys.connections],
    success: (c) => `已连接 GitHub 账号 ${c.login}`,
    onSuccess: () => {
      onOpenChange(false);
      setToken("");
      setName("");
    },
  });
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="添加 GitHub 访问令牌"
      description={
        <>
          在 GitHub 上{" "}
          <a href={TOKEN_URL} target="_blank" rel="noreferrer" className="text-primary hover:underline">
            新建一个 fine-grained token
          </a>
          ，选择要给团队用的仓库，Repository permissions 里把 Contents 和 Pull requests 设为 Read and write。也可以用经典令牌（repo 权限）。
        </>
      }
      submitLabel="验证并保存"
      busy={add.isPending}
      disabled={!token.trim()}
      onSubmit={() => add.mutate()}
    >
      <Field label="访问令牌">
        <Input autoFocus type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder="github_pat_…" className="font-mono" />
      </Field>
      <Field label="名称（可选）" hint="留空时用 GitHub 账号名。">
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：公司组织" />
      </Field>
    </FormDialog>
  );
}
