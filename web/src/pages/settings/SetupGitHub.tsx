// Deployment step (admin): register the platform GitHub App via GitHub's
// manifest flow. Not part of the daily product, linked from Admin.
import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { useState } from "react";
import { git } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useGitHubApp } from "@/api/queries";
import type { GitHubAppStatus } from "@/api/views";
import { Card, CardTitle, StatusPill } from "@/components/app/bits";
import { ConfirmAction, Field, KeyValueList, Loading, Spinner } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NavButton } from "@/components/shell/Shell";
import { useFlash } from "@/lib/flash";

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

export function SetupGitHubPage() {
  const app = useGitHubApp();
  const qc = useQueryClient();
  useFlash({ github_app: (slug) => `GitHub App ${slug} 已创建，用户现在可以一键连接 GitHub` }, () => qc.invalidateQueries({ queryKey: keys.githubApp }));
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[760px] flex-col gap-5">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <h1 className="text-2xl font-semibold">部署设置 · GitHub App</h1>
            <p className="text-[13px] text-muted-foreground">部署后做一次。之后用户只在“GitHub 连接”页点“连接 GitHub”。</p>
          </div>
        </div>
        {app.data ? <AppPanel status={app.data} /> : <Loading />}
      </div>
    </main>
  );
}

function AppPanel({ status }: { status: GitHubAppStatus }) {
  const [organization, setOrganization] = useState("");
  const create = useAction(() => git.createApp(organization.trim()), { onSuccess: ({ action, manifest }) => postToGitHub(action, manifest) });
  const forget = useAction(() => git.forgetApp(), { invalidate: [keys.githubApp], success: "已移除" });
  if (status.configured && status.app) {
    const a = status.app;
    const perms = Object.entries(a.permissions ?? {}).map(([k, v]) => `${k}: ${v}`).join("、") || "—";
    return (
      <Card className="gap-4">
        <CardTitle action={<StatusPill tone="#2E7D4F" label="已创建" />}>GitHub App</CardTitle>
        <KeyValueList
          items={[
            { label: "名称", value: a.name },
            { label: "所属账号", value: a.owner },
            { label: "标识", value: a.slug, mono: true },
            { label: "权限", value: perms },
            { label: "Webhook", value: a.webhook_active ? "已启用" : "未启用" },
          ]}
        />
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline">
            <a href={a.html_url} target="_blank" rel="noreferrer">
              在 GitHub 上查看 <ExternalLink />
            </a>
          </Button>
          <ConfirmAction title="移除平台的 GitHub App？" description="只从平台里移除记录，已有的 App 连接会失效。GitHub 上的 App 需要你自己去删除。" confirmLabel="移除" onConfirm={() => forget.mutate()} />
        </div>
      </Card>
    );
  }
  return (
    <Card tone="ask" className="gap-4">
      <CardTitle action={<StatusPill tone="#C2570C" label="未创建" />}>创建 GitHub App</CardTitle>
      <Field label="创建在哪个组织下（可选，留空则创建在你自己的 GitHub 账号下）">
        <Input placeholder="组织名，例如 my-org" value={organization} onChange={(e) => setOrganization(e.target.value)} />
      </Field>
      <div>
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending && <Spinner />} 一键创建 GitHub App
        </Button>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">会跳到 GitHub 的创建确认页，点“Create GitHub App”后自动回来。权限：代码读写、PR 读写、工作流文件写入、读取 CI 检查和状态。</p>
    </Card>
  );
}
