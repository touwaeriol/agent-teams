import { useQuery } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { Link } from "react-router-dom";
import { errorText, Field, FieldGroup, Loading } from "@/components/common";
import { Combobox, type ComboOption } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, type Project, type Repo } from "@/lib/api";
import { keys, useConnections } from "@/lib/queries";

export interface ProjectDraft {
  name: string;
  description: string;
  context: string;
  git_connection_id: string;
  repo: string;
  branch: string;
}

export const emptyDraft: ProjectDraft = { name: "", description: "", context: "", git_connection_id: "", repo: "", branch: "" };

export function draftOf(p: Project): ProjectDraft {
  return { name: p.name, description: p.description, context: p.context, repo: p.repo ?? "", branch: p.branch, git_connection_id: p.git_connection_id ?? "" };
}

const NO_REPO = "__none__";

/** The editable fields of a project, shared by "new project" and project settings. */
export function ProjectFields({ draft, onChange }: { draft: ProjectDraft; onChange: (draft: ProjectDraft) => void }) {
  const set = (patch: Partial<ProjectDraft>) => onChange({ ...draft, ...patch });
  return (
    <div className="flex flex-col gap-5">
      <Field label="项目名称">
        <Input required placeholder="例如：官网改版" value={draft.name} onChange={(e) => set({ name: e.target.value })} />
      </Field>
      <Field label="一句话简介">
        <Input placeholder="这个项目是做什么的" value={draft.description} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <Field label="项目上下文" hint="负责人和每个员工开工前都会读到这段：技术栈、约定、不能动的地方、怎么跑测试……支持 Markdown。">
        <Textarea
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
  if (connections.isLoading) return <Loading className="p-4" />;
  return (
    <FieldGroup legend="代码仓库（GitHub）">
      {list.length === 0 ? (
        <p className="text-[13px] leading-5 text-muted-foreground">
          还没有 Git 连接。
          <Link to="/settings/git" className="text-brand hover:underline">
            先去连接 GitHub
          </Link>
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
    </FieldGroup>
  );
}

function RepoPicker({ connectionId, draft, set }: { connectionId: string; draft: ProjectDraft; set: (patch: Partial<ProjectDraft>) => void }) {
  const repos = useQuery({ queryKey: keys.repos(connectionId), queryFn: () => api<Repo[]>(`/api/git/connections/${connectionId}/repos`) });
  const repo = draft.repo.trim();
  const branches = useQuery({
    queryKey: ["branches", connectionId, repo],
    queryFn: () => api<string[]>(`/api/git/connections/${connectionId}/branches?repo=${encodeURIComponent(repo)}`),
    enabled: Boolean(repo),
    retry: false,
  });
  const defaultBranch = repos.data?.find((r) => r.full_name === repo)?.default_branch;
  const repoOptions: ComboOption[] = (repos.data ?? []).map((r) => ({
    value: r.full_name,
    label: <span className="font-mono text-[13px]">{r.full_name}</span>,
    hint: r.private ? <Lock className="size-3.5" aria-label="私有" /> : undefined,
    keywords: r.description,
  }));
  const branchOptions: ComboOption[] = (branches.data ?? []).map((b) => ({
    value: b,
    label: <span className="font-mono text-[13px]">{b}</span>,
    hint: b === defaultBranch ? "默认分支" : undefined,
  }));
  return (
    <div className="grid gap-3 sm:grid-cols-[3fr_2fr]">
      <Field label="仓库" hint={repos.error ? errorText(repos.error) : "输入搜索，或直接粘贴仓库链接 / owner/repo。"}>
        <Combobox
          value={draft.repo}
          onChange={(v) => set({ repo: v, branch: repos.data?.find((r) => r.full_name === v)?.default_branch ?? "" })}
          options={repoOptions}
          loading={repos.isLoading}
          placeholder="搜索仓库"
          empty="没有匹配的仓库"
          allowCustom
        />
      </Field>
      <Field
        label="基准分支"
        hint={branches.error ? errorText(branches.error) : "存放最终代码的分支，员工的成果最终合并到这里。"}
      >
        <Combobox
          value={draft.branch}
          onChange={(v) => set({ branch: v })}
          options={branchOptions}
          loading={branches.isLoading && Boolean(repo)}
          disabled={!repo}
          placeholder={repo ? "选择分支" : "先选仓库"}
          empty="没有匹配的分支"
          allowCustom
        />
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
