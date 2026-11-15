// Repository fields shared by NewProject and ProjectSettings: the Git
// connection, a searchable repo list (with “已被项目使用”) and the base branch.
import { Lock } from "lucide-react";
import { Link } from "react-router-dom";
import { useBranches, useConnections, useRepos } from "@/api/queries";
import type { GitConnectionView } from "@/api/views";
import { errorText, Field } from "@/components/common";
import { Combobox, type ComboOption } from "@/components/ui/combobox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface RepoDraft {
  git_connection_id: string;
  repo: string;
  branch: string;
}

const NONE = "__none__";

/** owner/repo from a pasted GitHub URL or the text as typed. */
export function normalizeRepo(text: string): string {
  const t = text.trim().replace(/\.git$/, "");
  const m = t.match(/github\.com[/:]([^/\s]+\/[^/\s#?]+)/);
  return m ? m[1] : t;
}

export function repoPayload(d: RepoDraft) {
  const repo = d.git_connection_id ? normalizeRepo(d.repo) : "";
  return { repo: repo || null, branch: repo ? d.branch.trim() : "", git_connection_id: repo ? d.git_connection_id : null };
}

function connectionLabel(c: GitConnectionView): string {
  return c.kind === "app" ? `${c.login}（GitHub App）` : `${c.name || c.login}（令牌）`;
}

export function RepoFields({ draft, onChange, layout = "new", currentProject }: { draft: RepoDraft; onChange: (d: RepoDraft) => void; layout?: "new" | "settings"; currentProject?: string }) {
  const connections = useConnections();
  const list = connections.data ?? [];
  const set = (patch: Partial<RepoDraft>) => onChange({ ...draft, ...patch });
  if (connections.isLoading) return null;
  if (list.length === 0)
    return (
      <p className="text-[13px] leading-6 text-muted-foreground">
        还没有连接 GitHub。
        <Link to="/settings/git" className="text-foreground underline underline-offset-2">
          先去连接
        </Link>
        ，就能在这里选仓库；也可以先不关联仓库。
      </p>
    );
  const connection = list.find((c) => c.id === draft.git_connection_id);
  const showPicker = layout === "settings" || list.length > 1;
  return (
    <div className={cn("grid gap-3", layout === "settings" ? "sm:grid-cols-3" : "sm:grid-cols-[3fr_2fr]")}>
      {showPicker && (
        <Field label="GitHub 连接" className={layout === "new" ? "sm:col-span-2" : undefined}>
          <Select value={draft.git_connection_id || NONE} onValueChange={(v) => set(v === NONE ? { git_connection_id: "", repo: "", branch: "" } : { git_connection_id: v, repo: "", branch: "" })}>
            <SelectTrigger>
              <SelectValue placeholder="选择连接" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE}>不关联仓库</SelectItem>
              {list.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {connectionLabel(c)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      {draft.git_connection_id ? (
        <RepoAndBranch draft={draft} set={set} connection={connection} layout={layout} currentProject={currentProject} />
      ) : (
        layout === "settings" && <p className="self-end pb-2.5 text-[13px] text-muted-foreground sm:col-span-2">未关联仓库：员工只能在空目录里工作。</p>
      )}
    </div>
  );
}

function RepoAndBranch({ draft, set, connection, layout, currentProject }: { draft: RepoDraft; set: (p: Partial<RepoDraft>) => void; connection?: GitConnectionView; layout: "new" | "settings"; currentProject?: string }) {
  const repos = useRepos(draft.git_connection_id);
  const repo = normalizeRepo(draft.repo);
  const branches = useBranches(draft.git_connection_id, repo);
  const items = repos.data?.items ?? [];
  const defaultBranch = items.find((r) => r.full_name === repo)?.default_branch;
  const repoOptions: ComboOption[] = items.map((r) => {
    const used = (r.used_by_projects ?? []).filter((p) => p.id !== currentProject);
    return {
      value: r.full_name,
      label: <span className="font-mono text-[13px]">{r.full_name}</span>,
      hint: used.length ? `已被 ${used.map((p) => p.name).join("、")} 使用` : r.private ? <Lock className="size-3.5" aria-label="私有" /> : undefined,
      keywords: r.description,
    };
  });
  const branchOptions: ComboOption[] = (branches.data ?? []).map((b) => ({ value: b, label: <span className="font-mono text-[13px]">{b}</span>, hint: b === defaultBranch ? "默认分支" : undefined }));
  const owner = connection?.login ?? "";
  return (
    <>
      <Field label="仓库" hint={layout === "new" ? (repos.error ? errorText(repos.error) : `来自你连接的 GitHub（${owner}），输入即可搜索，也能粘贴链接。`) : undefined}>
        <Combobox
          value={draft.repo}
          onChange={(v) => {
            const full = normalizeRepo(v);
            set({ repo: full, branch: items.find((r) => r.full_name === full)?.default_branch ?? draft.branch });
          }}
          options={repoOptions}
          loading={repos.isLoading}
          placeholder="搜索仓库"
          empty="没有匹配的仓库"
          allowCustom
          className="[&_input]:font-mono [&_input]:text-[13px]"
        />
      </Field>
      <Field label="基准分支" hint={layout === "new" ? (branches.error ? errorText(branches.error) : "存放最终代码的分支，讨论串的 PR 合并到这里。") : undefined}>
        <Combobox
          value={draft.branch}
          onChange={(v) => set({ branch: v })}
          options={branchOptions}
          loading={branches.isLoading && Boolean(repo)}
          disabled={!repo}
          placeholder={repo ? "选择分支" : "先选仓库"}
          empty="没有匹配的分支"
          allowCustom
          className="[&_input]:font-mono [&_input]:text-[13px]"
        />
      </Field>
    </>
  );
}
