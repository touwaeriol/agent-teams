import { useQuery } from "@tanstack/react-query";
import { Check, Lock, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { errorText, Field, Spinner } from "@/components/shared";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, type Repo } from "@/lib/api";
import { keys, useConnections } from "@/lib/queries";
import { cn } from "@/lib/utils";

export interface ProjectDraft {
  name: string;
  description: string;
  context: string;
  git_connection_id: string;
  repo: string;
  branch: string;
}

export const emptyDraft: ProjectDraft = { name: "", description: "", context: "", git_connection_id: "", repo: "", branch: "" };

const NO_REPO = "__none__";

export function ProjectFields({ draft, onChange }: { draft: ProjectDraft; onChange: (draft: ProjectDraft) => void }) {
  const set = (patch: Partial<ProjectDraft>) => onChange({ ...draft, ...patch });
  return (
    <div className="flex flex-col gap-5">
      <Field label="项目名称" htmlFor="p-name">
        <Input id="p-name" required placeholder="例如：官网改版" value={draft.name} onChange={(e) => set({ name: e.target.value })} />
      </Field>
      <Field label="一句话简介" htmlFor="p-desc">
        <Input id="p-desc" placeholder="这个项目是做什么的" value={draft.description} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <Field
        label="项目上下文"
        htmlFor="p-context"
        hint="负责人和每个员工开工前都会读到这段：技术栈、约定、不能动的地方、怎么跑测试……支持 Markdown。"
      >
        <Textarea
          id="p-context"
          rows={6}
          placeholder={"例如：\n- 前端 React + Vite，包管理用 pnpm\n- 改完跑 pnpm test，不要动 legacy/ 目录\n- 提交信息用中文"}
          value={draft.context}
          onChange={(e) => set({ context: e.target.value })}
        />
      </Field>
      <RepoFields draft={draft} set={set} />
    </div>
  );
}

function RepoFields({ draft, set }: { draft: ProjectDraft; set: (patch: Partial<ProjectDraft>) => void }) {
  const connections = useConnections();
  const list = connections.data ?? [];
  if (connections.isLoading) return <Spinner />;
  return (
    <fieldset className="flex flex-col gap-3 rounded-xl border bg-raised/40 p-4">
      <legend className="px-1 text-[13px] font-medium text-foreground/85">代码仓库（GitHub）</legend>
      {list.length === 0 ? (
        <p className="text-[13px] leading-5 text-muted-foreground">
          还没有 Git 连接。<Link to="/settings/git" className="text-primary hover:underline">先去添加一个 GitHub 访问令牌</Link>
          ，就能在这里选择仓库；也可以先不关联仓库。
        </p>
      ) : (
        <>
          <Field label="使用的 Git 连接">
            <Select
              value={draft.git_connection_id || NO_REPO}
              onValueChange={(v) => set(v === NO_REPO ? { git_connection_id: "", repo: "", branch: "" } : { git_connection_id: v })}
            >
              <SelectTrigger>
                <SelectValue placeholder="选择连接" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_REPO}>不关联仓库</SelectItem>
                {list.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {draft.git_connection_id && <RepoPicker connectionId={draft.git_connection_id} draft={draft} set={set} />}
        </>
      )}
    </fieldset>
  );
}

function RepoPicker({ connectionId, draft, set }: { connectionId: string; draft: ProjectDraft; set: (patch: Partial<ProjectDraft>) => void }) {
  const [filter, setFilter] = useState("");
  const repos = useQuery({
    queryKey: keys.repos(connectionId),
    queryFn: () => api<Repo[]>(`/api/git/connections/${connectionId}/repos`),
  });
  const shown = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return (repos.data ?? []).filter((r) => !needle || r.full_name.toLowerCase().includes(needle)).slice(0, 50);
  }, [repos.data, filter]);

  return (
    <div className="flex flex-col gap-3">
      <Field label="仓库" hint="从列表里选，或直接粘贴 GitHub 链接 / owner/repo。">
        <Input placeholder="https://github.com/owner/repo" value={draft.repo} onChange={(e) => set({ repo: e.target.value, branch: "" })} />
      </Field>
      <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input className="pl-9" placeholder="在可访问的仓库里搜索" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="搜索仓库" />
      </div>
      <div className="scroll-thin max-h-56 overflow-y-auto rounded-lg border bg-well">
        {repos.isLoading && <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground"><Spinner className="size-3" />正在读取仓库列表</div>}
        {repos.error && <div className="p-3 text-xs text-destructive">{errorText(repos.error)}</div>}
        {repos.data && shown.length === 0 && <div className="p-3 text-xs text-muted-foreground">没有匹配的仓库</div>}
        {shown.map((r) => {
          const picked = draft.repo === r.full_name;
          return (
            <button
              type="button"
              key={r.full_name}
              onClick={() => set({ repo: r.full_name, branch: r.default_branch })}
              className={cn(
                "flex w-full items-center gap-2 border-b border-border/60 px-3 py-2 text-left last:border-0 hover:bg-raised",
                picked && "bg-accent text-accent-foreground hover:bg-accent",
              )}
            >
              <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{r.full_name}</span>
              {r.private && <Lock className="size-3.5 text-muted-foreground" aria-label="私有" />}
              <span className="text-[11px] text-muted-foreground">{r.default_branch}</span>
              {picked && <Check className="size-4" />}
            </button>
          );
        })}
      </div>
      <Field label="基准分支" hint="留空用仓库的默认分支。员工从这里切出自己的分支，不会直接推它。">
        <Input placeholder="main" value={draft.branch} onChange={(e) => set({ branch: e.target.value })} />
      </Field>
    </div>
  );
}

export function draftPayload(draft: ProjectDraft) {
  const repo = draft.git_connection_id ? draft.repo.trim() : "";
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    context: draft.context,
    repo: repo || null,
    branch: repo ? draft.branch.trim() : "",
    git_connection_id: repo ? draft.git_connection_id : null,
  };
}
