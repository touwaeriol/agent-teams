import { useQuery } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Github, Lock, Zap } from "lucide-react";
import { useState } from "react";
import { ConfirmAction, EmptyState, errorText, KeyValueList, Loading, Page, Panel, PickList, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { api, type GitConnection, type Repo } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { useFlash } from "@/lib/flash";
import { keys, useConnections, useGitHubApp } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

export function GitConnectionsPage() {
  const connections = useConnections();
  const app = useGitHubApp();
  const client = useQueryClient();
  useFlash({ connected: (name) => `已连接：${name}` }, () => client.invalidateQueries({ queryKey: keys.connections }));
  const ready = app.data?.configured;
  const connect = ready ? (
    <Button asChild>
      <a href={app.data!.install_url}>
        <Zap /> 连接 GitHub
      </a>
    </Button>
  ) : null;
  return (
    <Page
      title="Git 连接"
      description="点“连接 GitHub”跳到 GitHub，选你的账号或组织、勾选仓库，授权后自动回来。你的所有项目和节点共用这个授权；员工每次开工现取 1 小时有效的令牌，只能访问你勾选的仓库，不写进节点的文件。"
      actions={connect}
    >
      {connections.isLoading && <Loading />}
      {connections.data?.length === 0 && (
        <EmptyState
          icon={<Github />}
          title="还没有 Git 连接"
          description={ready ? "点“连接 GitHub”，在 GitHub 上选好仓库授权，回来就能在新建项目时选择仓库。" : "GitHub 授权还没开通，请联系管理员。"}
          action={connect}
        />
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        {connections.data?.map((c) => <ConnectionCard key={c.id} connection={c} />)}
      </div>
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
    <Panel
      as="article"
      title={connection.name}
      icon={<Github />}
      action={<StatusBadge tone="ok" label="正常" />}
      description={
        connection.kind === "app" ? "一键连接 · 每次运行现取 1 小时令牌，只含你授权的仓库" : "访问令牌 · 加密保存，经运行环境下发给员工，不落盘"
      }
    >
      <KeyValueList
        items={[
          { label: connection.kind === "app" ? "安装到" : "GitHub 账号", value: connection.login },
          { label: "添加时间", value: timeAgo(connection.created_at) },
        ]}
      />
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => setShowRepos(!showRepos)}>
          {showRepos ? "收起仓库" : "查看可访问的仓库"}
        </Button>
        {connection.kind === "app" && (
          <Button asChild variant="outline">
            <a href={`https://github.com/settings/installations/${connection.installation_id}`} target="_blank" rel="noreferrer">
              在 GitHub 上调整仓库 <ExternalLink />
            </a>
          </Button>
        )}
        <ConfirmAction
          title={`删除连接「${connection.name}」？`}
          description={
            connection.kind === "app"
              ? "只删除这里的连接；要彻底收回授权，在 GitHub 的 Settings → Applications 里卸载。用它的项目会失去仓库凭证。"
              : "用它的项目会失去仓库凭证，员工将无法再拉取或推送私有仓库，直到你在项目设置里换一个连接。"
          }
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
