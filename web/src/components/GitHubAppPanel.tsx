import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Github, Zap } from "lucide-react";
import { useState } from "react";
import { ConfirmAction, Field, KeyValueList, Panel, Spinner, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, type GitHubAppStatus } from "@/lib/api";
import { useFlash } from "@/lib/flash";
import { useAction } from "@/lib/mutations";
import { keys } from "@/lib/queries";
/** The platform's GitHub App: created once here, then every user connects with one click. */
export function GitHubAppPanel({ status }: { status: GitHubAppStatus }) {
  const client = useQueryClient();
  const [organization, setOrganization] = useState("");
  useFlash({ github_app: (slug) => `GitHub App ${slug} 已创建，用户现在可以一键连接 GitHub` }, () => client.invalidateQueries({ queryKey: keys.githubApp }));

  const create = useAction(() => api<{ action: string; manifest: string }>("/api/admin/github-app/manifest", { method: "POST", json: { organization } }), {
    onSuccess: ({ action, manifest }) => postToGitHub(action, manifest),
  });
  const forget = useAction(() => api("/api/admin/github-app", { method: "DELETE" }), { invalidate: [keys.githubApp], success: "已移除" });

  if (status.configured && status.app) {
    const info = status.app;
    return (
      <Panel title="平台 GitHub App（仅管理员可见）" icon={<Github />} action={<StatusBadge tone="ok" label="已创建" />} description="所有用户点上面的“连接 GitHub”时安装的就是这个 App。">
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
    <Panel tone="highlight" title="先创建平台的 GitHub App（只需一次）" icon={<Github />} action={<StatusBadge tone="warn" label="未创建" />} description="创建后，这里会变成“连接 GitHub”按钮：每个用户点一下就能把自己账号或组织的仓库授权给平台，不用再手动建令牌。">
      <div className="flex flex-wrap items-end gap-3">
        <Field label="创建在哪个组织下（可选，留空则创建在你自己的 GitHub 账号下）" className="w-full max-w-md">
          <Input placeholder="组织名，例如 my-org" value={organization} onChange={(e) => setOrganization(e.target.value)} />
        </Field>
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? <Spinner /> : <Zap />} 一键创建 GitHub App
        </Button>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">会跳到 GitHub 的创建确认页，点“Create GitHub App”后自动回到这一页。权限：代码读写、PR 读写、工作流文件写入。</p>
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
