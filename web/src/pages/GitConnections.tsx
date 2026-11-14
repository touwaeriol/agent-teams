import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Github, Lock, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { EmptyState, errorText, Field, PageHeader, Spinner } from "@/components/shared";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api, type GitConnection, type Repo } from "@/lib/api";
import { keys, useConnections } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

const TOKEN_URL =
  "https://github.com/settings/personal-access-tokens/new?name=agent-teams&description=Agent%20Teams&contents=write&pull_requests=write&metadata=read";

export function GitConnectionsPage() {
  const connections = useConnections();
  const [adding, setAdding] = useState(false);
  return (
    <div className="container flex flex-col gap-5 py-6">
      <PageHeader
        title="Git 连接"
        description="在这里授权一次，所有项目和节点共用。令牌加密保存在服务端，不写进节点的文件；员工运行时 git 和 gh 通过环境变量拿到它，可以拉取、推送分支和建 PR。第一版只支持 GitHub。"
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus /> 添加 GitHub 连接
          </Button>
        }
      />
      {connections.isLoading && <Spinner />}
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
      <section className="grid gap-3 sm:grid-cols-3">
        <Option title="GitHub 访问令牌" status="可用" tone="ok" text="粘贴一个 fine-grained personal access token，勾选需要的仓库，权限给 Contents 和 Pull requests 读写。配置最快。" />
        <Option title="GitHub App" status="计划中" tone="neutral" text="平台一键创建 App，安装到组织并选择仓库；按员工身份区分读写，令牌 1 小时过期。" />
        <Option title="GitLab · 其他平台 · SSH" status="以后" tone="neutral" text="暂不支持。" />
      </section>
      <AddConnectionDialog open={adding} onOpenChange={setAdding} />
    </div>
  );
}

function Option({ title, status, tone, text }: { title: string; status: string; tone: "ok" | "neutral"; text: string }) {
  return (
    <div className="flex flex-col gap-2 rounded-xl border bg-panel p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="font-semibold">{title}</span>
        <Badge tone={tone}>{status}</Badge>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">{text}</p>
    </div>
  );
}

function ConnectionCard({ connection }: { connection: GitConnection }) {
  const client = useQueryClient();
  const [showRepos, setShowRepos] = useState(false);
  const repos = useQuery({
    queryKey: keys.repos(connection.id),
    queryFn: () => api<Repo[]>(`/api/git/connections/${connection.id}/repos`),
    enabled: showRepos,
  });
  const remove = useMutation({
    mutationFn: () => api(`/api/git/connections/${connection.id}`, { method: "DELETE" }),
    onSuccess: () => {
      client.invalidateQueries({ queryKey: keys.connections });
      client.invalidateQueries({ queryKey: keys.projects });
      toast.success("连接已删除");
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <article className="flex flex-col gap-3 rounded-xl border bg-panel p-5">
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-[15px] font-semibold">
          <Github className="size-4" /> {connection.name}
        </span>
        <Badge tone="ok">正常</Badge>
      </div>
      <span className="text-[13px] text-muted-foreground">访问令牌 · 加密保存，经运行环境下发给员工，不落盘</span>
      <dl className="flex flex-col gap-1.5 text-[13px]">
        <Row k="GitHub 账号" v={connection.login} />
        <Row k="添加时间" v={timeAgo(connection.created_at)} />
      </dl>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => setShowRepos(!showRepos)}>
          {showRepos ? "收起仓库" : "查看可访问的仓库"}
        </Button>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive">
              <Trash2 /> 删除
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>删除连接「{connection.name}」？</AlertDialogTitle>
              <AlertDialogDescription>用它的项目会失去仓库凭证，员工将无法再拉取或推送私有仓库，直到你在项目设置里换一个连接。</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction onClick={() => remove.mutate()}>删除</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
      {showRepos && (
        <div className="scroll-thin max-h-64 overflow-y-auto rounded-lg border bg-well">
          {repos.isLoading && <div className="p-3"><Spinner /></div>}
          {repos.error && <div className="p-3 text-xs text-destructive">{errorText(repos.error)}</div>}
          {repos.data?.map((r) => (
            <a key={r.full_name} href={r.html_url} target="_blank" rel="noreferrer" className="flex items-center gap-2 border-b border-border/60 px-3 py-2 last:border-0 hover:bg-raised">
              <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{r.full_name}</span>
              {r.private && <Lock className="size-3.5 text-muted-foreground" />}
              <span className="text-[11px] text-muted-foreground">{r.default_branch}</span>
              <ExternalLink className="size-3 text-muted-foreground" />
            </a>
          ))}
        </div>
      )}
    </article>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between gap-3 border-b border-border/60 pb-1.5">
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="font-mono text-xs">{v}</dd>
    </div>
  );
}

function AddConnectionDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const client = useQueryClient();
  const add = useMutation({
    mutationFn: () => api<GitConnection>("/api/git/connections", { method: "POST", json: { token: token.trim(), name: name.trim() } }),
    onSuccess: (c) => {
      client.invalidateQueries({ queryKey: keys.connections });
      toast.success(`已连接 GitHub 账号 ${c.login}`);
      onOpenChange(false);
      setToken("");
      setName("");
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    add.mutate();
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>添加 GitHub 访问令牌</DialogTitle>
            <DialogDescription>
              在 GitHub 上{" "}
              <a href={TOKEN_URL} target="_blank" rel="noreferrer" className="text-primary hover:underline">
                新建一个 fine-grained token
              </a>
              ，选择要给团队用的仓库，Repository permissions 里把 Contents 和 Pull requests 设为 Read and write。也可以用经典令牌（repo 权限）。
            </DialogDescription>
          </DialogHeader>
          <Field label="访问令牌">
            <Input autoFocus type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder="github_pat_…" className="font-mono" />
          </Field>
          <Field label="名称（可选）" hint="留空时用 GitHub 账号名。">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：公司组织" />
          </Field>
          <DialogFooter>
            <Button type="submit" disabled={!token.trim() || add.isPending}>
              {add.isPending && <Spinner />} 验证并保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
