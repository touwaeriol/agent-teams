import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Github, Zap } from "lucide-react";
import { useState } from "react";
import { ConfirmAction, DataTable, Field, KeyValueList, Loading, Page, Panel, Spinner, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api, type AdminUser, type PlatformSettings } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { useFlash } from "@/lib/flash";
import { keys, useGitHubApp, useMe } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

const ADMIN = ["admin"];

export function AdminPage() {
  const me = useMe();
  const users = useQuery({ queryKey: [...ADMIN, "users"], queryFn: () => api<AdminUser[]>("/api/admin/users") });
  const settings = useQuery({ queryKey: [...ADMIN, "settings"], queryFn: () => api<PlatformSettings>("/api/admin/settings") });

  const saveSettings = useAction((s: PlatformSettings) => api<PlatformSettings>("/api/admin/settings", { method: "PUT", json: { ...s } }), {
    invalidate: [ADMIN],
    success: "已保存",
  });
  const update = useAction(({ id, changes }: { id: string; changes: Record<string, unknown> }) => api(`/api/admin/users/${id}`, { method: "PATCH", json: changes }), {
    invalidate: [ADMIN],
  });
  const remove = useAction((id: string) => api(`/api/admin/users/${id}`, { method: "DELETE" }), { invalidate: [ADMIN], success: "已删除" });

  return (
    <Page title="管理" description="平台设置、GitHub App 和用户。">
      <GitHubAppPanel />
      {settings.data && (
        <Panel title="平台设置">
          <div className="flex flex-wrap items-center gap-6">
            <label className="flex items-center gap-3">
              <Switch checked={settings.data.registration_open} onCheckedChange={(v) => saveSettings.mutate({ ...settings.data!, registration_open: v })} />
              <span>开放注册</span>
            </label>
            <label className="flex items-center gap-3">
              <span>每人默认最多节点数</span>
              <Input
                type="number"
                min={0}
                className="w-24"
                defaultValue={settings.data.default_max_nodes}
                onBlur={(e) => saveSettings.mutate({ ...settings.data!, default_max_nodes: Number(e.target.value) })}
              />
            </label>
          </div>
        </Panel>
      )}
      {users.isLoading ? (
        <Loading />
      ) : (
        <DataTable
          className="bg-panel"
          minWidth={760}
          rows={users.data ?? []}
          rowKey={(u) => u.id}
          columns={[
            {
              header: "邮箱",
              cell: (u) => (
                <>
                  {u.email}
                  {u.id === me.data?.id && <span className="ml-2 text-xs text-muted-foreground">（你）</span>}
                </>
              ),
            },
            {
              header: "角色",
              cell: (u) => (
                <Button size="sm" variant="outline" disabled={u.id === me.data?.id} onClick={() => update.mutate({ id: u.id, changes: { role: u.role === "admin" ? "user" : "admin" } })}>
                  {u.role === "admin" ? "管理员" : "成员"}
                </Button>
              ),
            },
            {
              header: "状态",
              cell: (u) => (
                <label className="flex items-center gap-2">
                  <Switch checked={u.active} disabled={u.id === me.data?.id} onCheckedChange={(v) => update.mutate({ id: u.id, changes: { active: v } })} />
                  <StatusBadge tone={u.active ? "ok" : "bad"} label={u.active ? "启用" : "停用"} />
                </label>
              ),
            },
            { header: "节点", cell: (u) => u.nodes },
            {
              header: "节点上限",
              cell: (u) => (
                <Input
                  type="number"
                  min={0}
                  className="h-8 w-24"
                  placeholder="默认"
                  defaultValue={u.max_nodes ?? ""}
                  onBlur={(e) => update.mutate({ id: u.id, changes: e.target.value === "" ? { clear_max_nodes: true } : { max_nodes: Number(e.target.value) } })}
                />
              ),
            },
            { header: "注册", cell: (u) => <span className="text-muted-foreground">{timeAgo(u.created_at)}</span> },
            {
              header: "",
              className: "text-right",
              cell: (u) =>
                u.id !== me.data?.id && (
                  <ConfirmAction iconOnly={`删除 ${u.email}`} title={`删除用户 ${u.email}？`} description="会同时删除他的节点、团队、项目和对话，无法恢复。" onConfirm={() => remove.mutate(u.id)} />
                ),
            },
          ]}
        />
      )}
    </Page>
  );
}

/** The platform's GitHub App: created once here, then every user connects with one click. */
function GitHubAppPanel() {
  const app = useGitHubApp();
  const client = useQueryClient();
  const [organization, setOrganization] = useState("");
  useFlash({ github_app: (slug) => `GitHub App ${slug} 已创建，用户现在可以一键连接 GitHub` }, () => client.invalidateQueries({ queryKey: keys.githubApp }));

  const create = useAction(() => api<{ action: string; manifest: string }>("/api/admin/github-app/manifest", { method: "POST", json: { organization } }), {
    onSuccess: ({ action, manifest }) => postToGitHub(action, manifest),
  });
  const forget = useAction(() => api("/api/admin/github-app", { method: "DELETE" }), { invalidate: [keys.githubApp], success: "已移除" });

  if (!app.data) return <Loading />;
  if (app.data.configured && app.data.app) {
    const info = app.data.app;
    return (
      <Panel title="GitHub App" icon={<Github />} action={<StatusBadge tone="ok" label="已创建" />} description="用户在“Git 连接”页点“连接 GitHub”，安装这个 App 并选择仓库即可。">
        <KeyValueList items={[{ label: "名称", value: info.name, mono: false }, { label: "所属账号", value: info.owner }, { label: "标识", value: info.slug }]} />
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline">
            <a href={info.html_url} target="_blank" rel="noreferrer">
              在 GitHub 上查看 <ExternalLink />
            </a>
          </Button>
          <ConfirmAction
            title="移除平台的 GitHub App？"
            description="只从平台里移除记录，已有的 App 连接会失效。GitHub 上的 App 需要你自己去删除。"
            confirmLabel="移除"
            onConfirm={() => forget.mutate()}
          />
        </div>
      </Panel>
    );
  }
  return (
    <Panel title="GitHub App" icon={<Github />} action={<StatusBadge tone="warn" label="未创建" />} description="创建后，用户点一下就能把 GitHub 账号或组织的仓库授权给平台，不用再手动建令牌。只需要做一次。">
      <div className="flex flex-wrap items-end gap-3">
        <Field label="创建在哪个组织下（可选，留空则创建在你自己的 GitHub 账号下）" className="w-full max-w-md">
          <Input placeholder="组织名，例如 my-org" value={organization} onChange={(e) => setOrganization(e.target.value)} />
        </Field>
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? <Spinner /> : <Zap />} 一键创建 GitHub App
        </Button>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">会跳到 GitHub 的创建确认页，点“Create GitHub App”后自动回到这里。权限：代码读写、PR 读写、工作流文件写入。</p>
    </Panel>
  );
}

/** GitHub's manifest flow: the browser posts the manifest to github.com. */
function postToGitHub(action: string, manifest: string) {
  const form = document.createElement("form");
  form.method = "post";
  form.action = action;
  const field = document.createElement("input");
  field.type = "hidden";
  field.name = "manifest";
  field.value = manifest;
  form.appendChild(field);
  document.body.appendChild(form);
  form.submit();
}
