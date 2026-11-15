// ProjectSettings board: repository, basics, work mode, approvals, project
// memory, archive/delete. Toggles save at once; text fields have a save button.
import { Pin, PinOff, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useMemory, useProject } from "@/api/queries";
import type { MemoryView, ProjectUpdate, ProjectView } from "@/api/views";
import { Card, CardTitle, ChoiceCards } from "@/components/app/bits";
import { RepoFields, repoPayload, type RepoDraft } from "@/components/app/repo";
import { ConfirmAction, EmptyState, errorText, Field, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { NavButton } from "@/components/shell/Shell";
import { cn } from "@/lib/utils";
import { useProjectId } from "./Main";

const MODES = [
  { id: "worktree" as const, title: "worktree（推荐）", desc: "每个节点一份主仓库，讨论串和成员各开 worktree。" },
  { id: "clone" as const, title: "独立克隆", desc: "每个成员一份完整克隆，最稳但占磁盘。" },
];

const MEMORY_SOURCE: Record<string, string> = { user: "你填写", coordinator: "统筹整理", lead: "组长总结" };

export function memorySource(m: Pick<MemoryView, "source">): string {
  const thread = m.source.match(/^thread:(\d+)$/);
  if (thread) return `#${thread[1]} 总结`;
  return MEMORY_SOURCE[m.source] ?? m.source;
}

export function ProjectSettingsPage() {
  const pid = useProjectId();
  const project = useProject(pid);
  if (project.error) return <EmptyState title="找不到这个项目" description={errorText(project.error)} />;
  if (!project.data) return <Loading className="h-full" />;
  return <Settings project={project.data} />;
}

function useUpdate(pid: string, success?: string) {
  return useAction((body: ProjectUpdate) => projects.update(pid, body), { invalidate: [keys.project(pid), keys.projects], success });
}

function Settings({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[860px] flex-col gap-5">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <Link to={`/projects/${project.id}/overview`} className="text-xs text-muted-foreground hover:text-foreground">
              {project.name}
            </Link>
            <h1 className="text-2xl font-semibold">项目设置</h1>
          </div>
        </div>
        <RepoSection project={project} />
        <BasicsSection project={project} />
        <Card className="gap-3.5">
          <CardTitle>工作方式</CardTitle>
          <ChoiceCards label="工作方式" value={project.work_mode} onChange={(v) => update.mutate({ work_mode: v })} options={MODES} />
          <div className="grid gap-3 sm:grid-cols-2">
            <ReadOnly label="讨论串分支命名" value="thread/{编号}-{标题}" mono />
            <ReadOnly label="讨论串结束后" value="清理 worktree（有未推送改动时保留并提醒）" />
          </div>
        </Card>
        <ApprovalSection project={project} />
        <MemorySection pid={project.id} />
        <DangerSection project={project} />
      </div>
    </main>
  );
}

function ReadOnly({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[13px] font-medium">{label}</span>
      <span className={cn("flex h-10 items-center rounded-lg border border-input px-3 text-[13px]", mono && "font-mono")}>{value}</span>
    </div>
  );
}

function RepoSection({ project }: { project: ProjectView }) {
  const initial: RepoDraft = { git_connection_id: project.git_connection_id ?? "", repo: project.repo ?? "", branch: project.branch };
  const [draft, setDraft] = useState(initial);
  useEffect(() => setDraft({ git_connection_id: project.git_connection_id ?? "", repo: project.repo ?? "", branch: project.branch }), [project.git_connection_id, project.repo, project.branch]);
  const update = useUpdate(project.id, "仓库设置已保存");
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const save = () => {
    const p = repoPayload(draft);
    update.mutate(p.repo ? p : { clear_repo: true, git_connection_id: null });
  };
  return (
    <Card className="gap-3.5">
      <CardTitle action={dirty && <Button size="sm" onClick={save} disabled={update.isPending}>保存</Button>}>代码仓库</CardTitle>
      <RepoFields draft={draft} onChange={setDraft} layout="settings" currentProject={project.id} />
    </Card>
  );
}

function BasicsSection({ project }: { project: ProjectView }) {
  const [name, setName] = useState(project.name);
  const [description, setDescription] = useState(project.description);
  const [context, setContext] = useState(project.context);
  const update = useUpdate(project.id, "已保存");
  const dirty = name !== project.name || description !== project.description || context !== project.context;
  return (
    <Card className="gap-3.5">
      <CardTitle action={dirty && <Button size="sm" disabled={!name.trim() || update.isPending} onClick={() => update.mutate({ name: name.trim(), description, context })}>保存</Button>}>
        基本信息
      </CardTitle>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="项目名称">
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="一句话简介">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </div>
      <Field label="项目上下文" hint="统筹、组长和每个成员开工前都会读到。">
        <Textarea rows={4} value={context} onChange={(e) => setContext(e.target.value)} />
      </Field>
    </Card>
  );
}

function SwitchRow({ title, desc, checked, onChange, danger }: { title: string; desc: string; checked: boolean; onChange: (v: boolean) => void; danger?: boolean }) {
  return (
    <label className="flex cursor-pointer items-center justify-between gap-4 border-t border-[#F1EFEA] py-2.5">
      <span className="flex flex-col gap-0.5">
        <span className="font-medium">{title}</span>
        <span className="text-xs text-muted-foreground">{desc}</span>
      </span>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={title} className={danger ? "data-[state=checked]:bg-destructive" : undefined} />
    </label>
  );
}

function ApprovalSection({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  const d = project.delivery;
  return (
    <Card className="gap-3">
      <CardTitle>审批</CardTitle>
      <div className="flex flex-col">
        <SwitchRow danger title="新讨论串默认“全部放行”" desc="打开后，讨论串里所有成员的操作都不再询问。适合你在旁边盯着的时候。" checked={project.auto_approve} onChange={(v) => update.mutate({ auto_approve: v })} />
        <SwitchRow title="成员提问时通知我" desc="员工用“问你”提问、或请求权限时，侧栏和项目首页都会提醒。" checked={d.notify_on_ask} onChange={(v) => update.mutate({ delivery: { notify_on_ask: v } })} />
        <SwitchRow title="讨论串完成后需要我验收" desc="关闭后 PR 一通过检查就自动合并到基准分支。" checked={project.require_acceptance} onChange={(v) => update.mutate({ require_acceptance: v })} />
        <SwitchRow title="允许员工修改 workflow" desc="允许成员改 .github/workflows 下的文件（默认不允许）。" checked={d.allow_workflow_edits} onChange={(v) => update.mutate({ delivery: { allow_workflow_edits: v } })} />
        <div className="flex items-center justify-between gap-4 border-t border-[#F1EFEA] py-2.5">
          <span className="flex flex-col gap-0.5">
            <span className="font-medium">合并方式</span>
            <span className="text-xs text-muted-foreground">讨论串 PR 合并到基准分支时用。</span>
          </span>
          <Select value={d.merge_method} onValueChange={(v) => update.mutate({ delivery: { merge_method: v as "squash" | "merge" | "rebase" } })}>
            <SelectTrigger className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="squash">squash</SelectItem>
              <SelectItem value="merge">merge commit</SelectItem>
              <SelectItem value="rebase">rebase</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
    </Card>
  );
}

function MemorySection({ pid }: { pid: string }) {
  const memory = useMemory(pid);
  const [adding, setAdding] = useState(false);
  const [text, setText] = useState("");
  const add = useAction(() => projects.addMemory(pid, text.trim()), {
    invalidate: [keys.memory(pid)],
    onSuccess: () => {
      setText("");
      setAdding(false);
    },
  });
  return (
    <Card className="gap-3">
      <div id="memory" className="flex items-center justify-between gap-3">
        <h2 className="text-[15px] font-semibold">项目记忆</h2>
        <Button variant="outline" size="sm" onClick={() => setAdding(true)}>
          添加一条
        </Button>
      </div>
      <p className="text-[13px] text-muted-foreground">讨论串结束时由组长总结、统筹整理。统筹、组长和成员开工时都会带上。你可以改、可以删。</p>
      {adding && (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (text.trim()) add.mutate();
          }}
        >
          <Textarea rows={2} autoFocus value={text} onChange={(e) => setText(e.target.value)} placeholder="例如：改完前端要跑 tsc 和 vite build" />
          <span className="flex gap-2">
            <Button size="sm" type="submit" disabled={!text.trim() || add.isPending}>
              添加
            </Button>
            <Button size="sm" variant="outline" type="button" onClick={() => setAdding(false)}>
              取消
            </Button>
          </span>
        </form>
      )}
      {memory.isLoading && <Loading />}
      {memory.data?.length === 0 && !adding && <p className="text-[13px] text-muted-foreground">还没有记忆。</p>}
      {(memory.data ?? []).map((m) => (
        <MemoryRow key={m.id} pid={pid} item={m} />
      ))}
    </Card>
  );
}

function MemoryRow({ pid, item }: { pid: string; item: MemoryView }) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(item.text);
  const invalidate = [keys.memory(pid)];
  const save = useAction((body: { text?: string; pinned?: boolean }) => projects.updateMemory(pid, item.id, body), { invalidate, onSuccess: () => setEditing(false) });
  const remove = useAction(() => projects.removeMemory(pid, item.id), { invalidate });
  if (editing)
    return (
      <div className="flex flex-col gap-2 rounded-lg bg-background px-3 py-2.5">
        <Textarea rows={2} autoFocus value={text} onChange={(e) => setText(e.target.value)} />
        <span className="flex gap-2">
          <Button size="sm" disabled={!text.trim() || save.isPending} onClick={() => save.mutate({ text: text.trim() })}>
            保存
          </Button>
          <Button size="sm" variant="outline" onClick={() => setEditing(false)}>
            取消
          </Button>
        </span>
      </div>
    );
  return (
    <div className="group flex items-start gap-3 rounded-lg bg-background px-3 py-2.5">
      <button type="button" onClick={() => setEditing(true)} className="flex-1 text-left text-[13px] leading-[21px]" title="点一下修改">
        {item.pinned && <Pin className="mr-1 inline size-3 text-[#C8622A]" />}
        {item.text}
      </button>
      <span className="whitespace-nowrap text-xs text-muted-foreground group-hover:hidden">{memorySource(item)}</span>
      <span className="hidden gap-1 group-hover:flex">
        <button type="button" aria-label={item.pinned ? "取消置顶" : "置顶"} onClick={() => save.mutate({ pinned: !item.pinned })} className="text-muted-foreground hover:text-foreground">
          {item.pinned ? <PinOff className="size-3.5" /> : <Pin className="size-3.5" />}
        </button>
        <button type="button" aria-label="删除" onClick={() => remove.mutate()} className="text-muted-foreground hover:text-destructive">
          <Trash2 className="size-3.5" />
        </button>
      </span>
    </div>
  );
}

function DangerSection({ project }: { project: ProjectView }) {
  const navigate = useNavigate();
  const update = useUpdate(project.id, project.archived ? "已恢复" : "已归档");
  const remove = useAction(() => projects.remove(project.id), { invalidate: [keys.projects], success: "项目已删除", onSuccess: () => navigate("/projects") });
  return (
    <Card tone="danger" className="flex-row flex-wrap items-center justify-between gap-3">
      <span className="flex flex-col gap-0.5">
        <span className="font-semibold">归档或删除项目</span>
        <span className="text-xs text-muted-foreground">归档后从侧栏收起，随时可以恢复；删除会连同讨论串记录一起删掉。</span>
      </span>
      <span className="flex gap-2">
        <Button variant="outline" onClick={() => update.mutate({ archived: !project.archived })}>
          {project.archived ? "恢复" : "归档"}
        </Button>
        <ConfirmAction title={`删除项目「${project.name}」？`} description="会关闭全部会话，连同讨论串记录一起删掉，不能恢复。" confirmLabel="删除" onConfirm={() => remove.mutate()}>
          删除
        </ConfirmAction>
      </span>
    </Card>
  );
}
