// Repositories of a project, shared by NewProject and ProjectSettings: an ordered
// list (remove, move up/down) and the searchable repo picker (with “已被项目使用”)
// to add more. Base branches are not set here: they belong in the project context.
import { ArrowDown, ArrowUp, Lock, X } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useConnections, useRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import { errorText, Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Combobox, type ComboOption } from "@/components/ui/combobox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

export interface RepoRef {
  repo: string;
  git_connection_id: string | null;
}

export const REPOS_HINT = "基准分支、分支约定等写在项目上下文里（例如“仓库 org/web 的基准分支：develop”），统筹和组长会按它开分支和 PR；没写的仓库用默认分支。";

/** owner/repo from a pasted GitHub URL or the text as typed. */
export function normalizeRepo(text: string): string {
  const t = text.trim().replace(/\.git$/, "");
  const m = t.match(/github\.com[/:]([^/\s]+\/[^/\s#?]+)/);
  return m ? m[1] : t;
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Appends a repository unless it is empty or already in the list. */
export function addRepo(list: RepoRef[], item: RepoRef): RepoRef[] {
  const repo = normalizeRepo(item.repo);
  if (!repo || !item.git_connection_id || list.some((r) => same(r.repo, repo))) return list;
  return [...list, { repo, git_connection_id: item.git_connection_id }];
}

export function removeRepo(list: RepoRef[], index: number): RepoRef[] {
  return list.filter((_, i) => i !== index);
}

/** Moves the repository at index by delta (-1 up, +1 down); out-of-range moves change nothing. */
export function moveRepo(list: RepoRef[], index: number, delta: number): RepoRef[] {
  const to = index + delta;
  if (to < 0 || to >= list.length) return list;
  const next = [...list];
  [next[index], next[to]] = [next[to], next[index]];
  return next;
}

/** The repos field of POST/PATCH /api/projects. */
export function reposPayload(list: RepoRef[]): RepoRef[] {
  return list.map((r) => ({ repo: normalizeRepo(r.repo), git_connection_id: r.git_connection_id })).filter((r) => r.repo && r.git_connection_id);
}

function connectionLabel(c: GitConnectionView): string {
  return c.kind === "app" ? `${c.login}（GitHub App）` : `${c.name || c.login}（令牌）`;
}

export function RepoList({ value, onChange, currentProject }: { value: RepoRef[]; onChange: (list: RepoRef[]) => void; currentProject?: string }) {
  const connections = useConnections();
  const list = connections.data ?? [];
  if (connections.isLoading) return null;
  if (list.length === 0 && value.length === 0)
    return (
      <p className="text-[13px] leading-6 text-muted-foreground">
        还没有连接 GitHub。
        <Link to="/settings/git" className="text-foreground underline underline-offset-2">
          先去连接
        </Link>
        ，就能在这里选仓库；也可以先不关联仓库。
      </p>
    );
  return (
    <div className="flex flex-col gap-3">
      {value.length > 0 ? (
        <ol className="flex flex-col divide-y rounded-lg border">
          {value.map((r, i) => (
            <RepoRow key={r.repo} item={r} index={i} count={value.length} connections={list} onChange={onChange} value={value} />
          ))}
        </ol>
      ) : (
        <p className="text-[13px] text-muted-foreground">未关联仓库：员工只能在空目录里工作。</p>
      )}
      {list.length > 0 && <AddRepo connections={list} value={value} onChange={onChange} currentProject={currentProject} />}
      <p className="text-[12px] leading-5 text-muted-foreground">{REPOS_HINT}</p>
    </div>
  );
}

function RepoRow({ item, index, count, connections, value, onChange }: { item: RepoRef; index: number; count: number; connections: GitConnectionView[]; value: RepoRef[]; onChange: (list: RepoRef[]) => void }) {
  const conn = connections.find((c) => c.id === item.git_connection_id);
  return (
    <li className="flex items-center gap-2 px-3 py-2">
      <span className="w-5 text-[12px] tabular-nums text-muted-foreground">{index + 1}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{item.repo}</span>
      <span className="truncate text-[12px] text-muted-foreground max-sm:hidden">{conn ? connectionLabel(conn) : "连接已删除"}</span>
      <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={`上移 ${item.repo}`} disabled={index === 0} onClick={() => onChange(moveRepo(value, index, -1))}>
        <ArrowUp className="size-4" />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={`下移 ${item.repo}`} disabled={index === count - 1} onClick={() => onChange(moveRepo(value, index, 1))}>
        <ArrowDown className="size-4" />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={`移除 ${item.repo}`} onClick={() => onChange(removeRepo(value, index))}>
        <X className="size-4" />
      </Button>
    </li>
  );
}

function AddRepo({ connections, value, onChange, currentProject }: { connections: GitConnectionView[]; value: RepoRef[]; onChange: (list: RepoRef[]) => void; currentProject?: string }) {
  const [connectionId, setConnectionId] = useState(connections[0]?.id ?? "");
  const repos = useRepos(connectionId);
  const items = repos.data?.items ?? [];
  const owner = connections.find((c) => c.id === connectionId)?.login ?? "";
  const options: ComboOption[] = items
    .filter((r) => !value.some((v) => same(v.repo, r.full_name)))
    .map((r) => {
      const used = (r.used_by_projects ?? []).filter((p) => p.id !== currentProject);
      return {
        value: r.full_name,
        label: <span className="font-mono text-[13px]">{r.full_name}</span>,
        hint: used.length ? `已被 ${used.map((p) => p.name).join("、")} 使用` : r.private ? <Lock className="size-3.5" aria-label="私有" /> : undefined,
        keywords: r.description,
      };
    });
  return (
    <div className="grid gap-3 sm:grid-cols-[2fr_3fr]">
      {connections.length > 1 && (
        <Field label="GitHub 连接">
          <Select value={connectionId} onValueChange={setConnectionId}>
            <SelectTrigger>
              <SelectValue placeholder="选择连接" />
            </SelectTrigger>
            <SelectContent>
              {connections.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {connectionLabel(c)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field label={value.length ? "再加一个仓库" : "添加仓库"} hint={repos.error ? errorText(repos.error) : `来自你连接的 GitHub（${owner}），输入即可搜索，也能粘贴链接；选中就加入列表。`} className={connections.length > 1 ? undefined : "sm:col-span-2"}>
        <Combobox
          value=""
          onChange={(v) => onChange(addRepo(value, { repo: v, git_connection_id: connectionId }))}
          options={options}
          loading={repos.isLoading}
          placeholder="搜索仓库"
          empty="没有匹配的仓库"
          allowCustom
          className="[&_input]:font-mono [&_input]:text-[13px]"
        />
      </Field>
    </div>
  );
}
