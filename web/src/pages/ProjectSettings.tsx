// 项目设置 as a dialog with a section nav (通用 / 记忆 / 环境 / 使用情况 / 高级).
// /projects/:pid/settings[/:section] opens it over the main board. Switches and
// selects save at once; text saves on blur (name, intro) or with its button (goal).
import { Archive, ArchiveRestore, BarChart3, Brain, Cloud, Pin, PinOff, Settings2, Trash2, Wrench } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useMemory, useNodes, useProjectUsage } from "@/api/queries";
import type { MemoryView, ProjectUpdate, ProjectView } from "@/api/views";
import { ChoiceCards, Dot } from "@/components/app/bits";
import { ProjectIcon } from "@/components/app/ProjectIcon";
import { RepoList, reposPayload, type RepoRef } from "@/components/app/repo";
import { ConfirmAction, Loading } from "@/components/common";
import { nodeSlots } from "@/components/shell/nodes";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { colorIcon, ICON_COLORS, ICON_EMOJIS } from "@/lib/projectIcon";
import { EFFORTS, fillDays, formatTokens, formatUSD, goalLimit, GOAL_MAX, modelOptions, SETTINGS_SECTIONS, type SettingsSection } from "@/lib/projectSettings";
import { cn } from "@/lib/utils";

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

const SECTION_ICONS: Record<SettingsSection, ReactNode> = {
  general: <Settings2 />,
  memory: <Brain />,
  environment: <Cloud />,
  usage: <BarChart3 />,
  advanced: <Wrench />,
};

function useUpdate(pid: string, success?: string) {
  return useAction((body: ProjectUpdate) => projects.update(pid, body), { invalidate: [keys.project(pid), keys.projects], success });
}

export function ProjectSettingsDialog({ project, section, onSection, onClose }: { project: ProjectView; section: SettingsSection; onSection: (s: SettingsSection) => void; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()} className="flex h-[min(800px,92vh)] max-w-[960px] flex-col gap-0 overflow-hidden rounded-[14px] p-0 outline-none">
        <div className="flex h-14 shrink-0 items-center border-b px-5">
          <DialogTitle className="text-[15px] font-medium">项目设置</DialogTitle>
          <DialogDescription className="sr-only">{project.name} 的设置</DialogDescription>
        </div>
        <div className="flex min-h-0 flex-1 max-md:flex-col">
          <nav aria-label="设置分区" className="scroll-thin flex shrink-0 gap-0.5 overflow-x-auto border-r p-3 max-md:border-b max-md:border-r-0 md:w-[200px] md:flex-col">
            {SETTINGS_SECTIONS.map((s) => (
              <button
                key={s.id}
                type="button"
                aria-current={section === s.id ? "page" : undefined}
                onClick={() => onSection(s.id)}
                className={cn("flex h-9 shrink-0 items-center gap-2.5 rounded-lg px-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-4 [&_svg]:shrink-0", section === s.id ? "bg-accent font-medium" : "text-foreground/80 hover:bg-raised")}
              >
                {SECTION_ICONS[s.id]}
                {s.label}
              </button>
            ))}
          </nav>
          <div className="scroll-thin min-h-0 flex-1 overflow-y-auto px-5 pb-8 pt-4">
            <Section section={section} project={project} onClose={onClose} />
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Section({ section, project, onClose }: { section: SettingsSection; project: ProjectView; onClose: () => void }) {
  switch (section) {
    case "memory":
      return <MemorySection pid={project.id} />;
    case "environment":
      return <EnvironmentSection project={project} />;
    case "usage":
      return <UsageSection pid={project.id} />;
    case "advanced":
      return <AdvancedSection project={project} />;
  }
  return <GeneralSection project={project} onClose={onClose} />;
}

function Heading({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex min-h-9 items-center justify-between gap-3 pb-2">
      <h3 className="font-medium">{children}</h3>
      {action}
    </div>
  );
}

function Row({ title, desc, children, className }: { title: ReactNode; desc?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-wrap items-center justify-between gap-x-4 gap-y-2 py-2.5", className)}>
      <span className="flex min-w-0 flex-1 basis-48 flex-col gap-0.5">
        <span>{title}</span>
        {desc && <span className="text-[13px] leading-5 text-muted-foreground">{desc}</span>}
      </span>
      {children && <span className="ml-auto flex max-w-full shrink-0 items-center gap-2">{children}</span>}
    </div>
  );
}

function SwitchRow({ title, desc, checked, onChange }: { title: string; desc?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <Row title={title} desc={desc}>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={title} className="data-[state=checked]:bg-[#1F6FD1]" />
    </Row>
  );
}

const Divider = () => <div className="my-3 h-px bg-border" />;

// ── 通用 ────────────────────────────────────────────────────────────────

function GeneralSection({ project, onClose }: { project: ProjectView; onClose: () => void }) {
  const update = useUpdate(project.id);
  return (
    <div className="flex flex-col">
      <Heading>通用</Heading>
      <Row title="名称和图标">
        <NameAndIcon project={project} />
      </Row>
      <Row title="一句话简介">
        <InlineText value={project.description} placeholder="在项目列表里显示" onSave={(description) => update.mutate({ description })} label="一句话简介" />
      </Row>
      <GoalField project={project} />
      <ModelFields project={project} />
      <Divider />
      <SwitchRow title="使用限额重置后自动继续讨论串" desc="Claude 使用限额用完时，受影响的讨论串会自动暂停；打开后到重置时间自动继续。" checked={project.auto_resume} onChange={(v) => update.mutate({ auto_resume: v })} />
      <Row title="归档项目" desc={project.archived ? "已归档：侧栏收在“已归档”里。" : "从侧栏收起，随时可以恢复。"}>
        <Button variant="outline" size="sm" onClick={() => update.mutate({ archived: !project.archived })}>
          {project.archived ? <ArchiveRestore /> : <Archive />}
          {project.archived ? "恢复" : "归档"}
        </Button>
      </Row>
      <span className="pt-3 text-[13px] text-muted-foreground">危险区域</span>
      <DeleteRow project={project} onClose={onClose} />
    </div>
  );
}

function NameAndIcon({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  return (
    <span className="flex h-9 w-[min(300px,60vw)] items-center gap-1 rounded-lg bg-raised pl-1 pr-1">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label="更换图标" className="flex size-7 shrink-0 items-center justify-center rounded-md hover:bg-background">
            <ProjectIcon project={project} size={22} />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-64 p-2">
          <DropdownMenuLabel>表情</DropdownMenuLabel>
          <div className="grid grid-cols-8 gap-0.5 px-1">
            {ICON_EMOJIS.map((e) => (
              <DropdownMenuItem key={e} onSelect={() => update.mutate({ icon: e })} className="justify-center p-1 text-lg" aria-label={`图标 ${e}`}>
                {e}
              </DropdownMenuItem>
            ))}
          </div>
          <DropdownMenuLabel>首字母配色</DropdownMenuLabel>
          <div className="flex flex-wrap gap-1.5 px-2 pb-1">
            {ICON_COLORS.map((c) => (
              <DropdownMenuItem key={c} onSelect={() => update.mutate({ icon: colorIcon(c) })} className="size-6 rounded-full p-0" style={{ background: c }} aria-label={`颜色 ${c}`} />
            ))}
          </div>
          <DropdownMenuItem onSelect={() => update.mutate({ icon: "" })} className="mt-1 text-[13px] text-muted-foreground">
            恢复默认
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <InlineText value={project.name} onSave={(name) => name.trim() && update.mutate({ name: name.trim() })} label="项目名称" bare required />
    </span>
  );
}

/** A text box that saves when it loses focus or on Enter (Escape puts the saved value back). */
function InlineText({ value, onSave, label, placeholder, bare, required }: { value: string; onSave: (v: string) => void; label: string; placeholder?: string; bare?: boolean; required?: boolean }) {
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  const commit = () => {
    if (text === value) return;
    if (required && !text.trim()) return setText(value);
    onSave(text);
  };
  return (
    <input
      aria-label={label}
      value={text}
      placeholder={placeholder}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
        if (e.key === "Escape") setText(value);
      }}
      className={cn("h-9 min-w-0 flex-1 bg-transparent px-2 outline-none", !bare && "w-[min(300px,60vw)] rounded-lg bg-raised focus:ring-2 focus:ring-ring/40")}
    />
  );
}

function GoalField({ project }: { project: ProjectView }) {
  const [text, setText] = useState(project.context);
  useEffect(() => setText(project.context), [project.context]);
  const update = useUpdate(project.id, "已保存");
  const limit = goalLimit(project.context);
  const dirty = text !== project.context;
  return (
    <div className="flex flex-col gap-2 py-2.5">
      <label htmlFor="project-goal">您想做些什么？</label>
      <Textarea
        id="project-goal"
        rows={4}
        value={text}
        maxLength={limit}
        onChange={(e) => setText(e.target.value)}
        placeholder="例如：重新设计新手引导流程，让更多新用户完成设置。统筹、组长和每个成员开工前都会读到这里；各仓库的基准分支、分支约定也写在这里。"
        className="resize-y border-0 bg-raised"
      />
      <span className="flex items-center justify-end gap-3 text-[13px] text-muted-foreground">
        {dirty && (
          <>
            <Button size="sm" variant="ghost" onClick={() => setText(project.context)}>
              取消
            </Button>
            <Button size="sm" disabled={update.isPending} onClick={() => update.mutate({ context: text })}>
              保存
            </Button>
          </>
        )}
        <span className="tabular-nums">
          {text.length.toLocaleString()} / {GOAL_MAX.toLocaleString()}
        </span>
      </span>
    </div>
  );
}

const ROLE = "__role";

function ModelSelect({ value, onChange, label, className }: { value: string; onChange: (v: string) => void; label: string; className?: string }) {
  return (
    <Select value={value || ROLE} onValueChange={(v) => onChange(v === ROLE ? "" : v)}>
      <SelectTrigger aria-label={label} className={cn("h-9 border-0 bg-raised", className)}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {modelOptions(value).map((o) => (
          <SelectItem key={o.value || ROLE} value={o.value || ROLE}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function ModelFields({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  return (
    <div className="flex flex-col pt-3">
      <span className="pb-1">模型</span>
      <span className="text-[13px] text-muted-foreground">跟随岗位设置时用岗位模板里的模型；改动对之后打开的会话生效。</span>
      <Row title="协调者">
        <ModelSelect label="协调者模型" value={project.coordinator_model} onChange={(v) => update.mutate({ coordinator_model: v })} className="w-[min(256px,50vw)]" />
      </Row>
      <Row title="讨论串" desc="组长和成员">
        <ModelSelect label="讨论串模型" value={project.thread_model} onChange={(v) => update.mutate({ thread_model: v })} className="w-[min(160px,32vw)]" />
        <Select value={project.thread_effort || ROLE} onValueChange={(v) => update.mutate({ thread_effort: v === ROLE ? "" : v })}>
          <SelectTrigger aria-label="讨论串思考强度" className="h-9 w-[104px] border-0 bg-raised">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {EFFORTS.map((e) => (
              <SelectItem key={e.value || ROLE} value={e.value || ROLE}>
                {e.value ? e.label : "强度跟随岗位"}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Row>
    </div>
  );
}

function DeleteRow({ project, onClose }: { project: ProjectView; onClose: () => void }) {
  const navigate = useNavigate();
  const remove = useAction(() => projects.remove(project.id), {
    invalidate: [keys.projects],
    success: "项目已删除",
    onSuccess: () => {
      onClose();
      navigate("/projects");
    },
  });
  return (
    <Row title="删除项目" desc="删除此项目及其所有讨论串、交付物和记忆。此操作无法撤销。">
      <ConfirmAction title={`删除项目「${project.name}」？`} description="会关闭全部会话，连同讨论串记录一起删掉，不能恢复。" confirmLabel="删除" size="sm" onConfirm={() => remove.mutate()}>
        <Trash2 /> 删除
      </ConfirmAction>
    </Row>
  );
}

// ── 记忆 ────────────────────────────────────────────────────────────────

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
    <div className="flex flex-col gap-3">
      <Heading
        action={
          <Button variant="outline" size="sm" onClick={() => setAdding(true)}>
            添加一条
          </Button>
        }
      >
        记忆
      </Heading>
      <p className="-mt-2 text-[13px] text-muted-foreground">讨论串结束时由组长总结、统筹整理。统筹、组长和成员开工时都会带上。你可以改、可以删。</p>
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
      <div className="flex flex-col gap-1.5">
        {(memory.data ?? []).map((m) => (
          <MemoryRow key={m.id} pid={pid} item={m} />
        ))}
      </div>
    </div>
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
      <div className="flex flex-col gap-2 rounded-xl bg-raised/60 px-3 py-2.5">
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
    <div className="group flex items-start gap-3 rounded-xl bg-raised/60 px-3 py-2.5">
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

// ── 环境 ────────────────────────────────────────────────────────────────

function repoRefs(project: ProjectView): RepoRef[] {
  return (project.repos ?? []).map((r) => ({ repo: r.repo, git_connection_id: r.git_connection_id ?? null }));
}

function EnvironmentSection({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  return (
    <div className="flex flex-col gap-2">
      <RepoSection project={project} />
      <Divider />
      <Heading>工作方式</Heading>
      <ChoiceCards label="工作方式" value={project.work_mode} onChange={(v) => update.mutate({ work_mode: v })} options={MODES} />
      <Row title="讨论串分支命名" desc="每个仓库各开一条讨论串分支">
        <span className="font-mono text-[13px] text-muted-foreground">thread/{"{编号}-{标题}"}</span>
      </Row>
      <Row title="讨论串结束后" desc="有未推送改动时保留并提醒">
        <span className="text-[13px] text-muted-foreground">清理 worktree</span>
      </Row>
      <Divider />
      <NodesList />
    </div>
  );
}

function RepoSection({ project }: { project: ProjectView }) {
  const initial = repoRefs(project);
  const [draft, setDraft] = useState(initial);
  const saved = JSON.stringify(initial);
  useEffect(() => setDraft(JSON.parse(saved) as RepoRef[]), [saved]);
  const update = useUpdate(project.id, "仓库设置已保存");
  const dirty = JSON.stringify(draft) !== saved;
  return (
    <div className="flex flex-col gap-2">
      <Heading
        action={
          dirty && (
            <Button size="sm" onClick={() => update.mutate({ repos: reposPayload(draft) })} disabled={update.isPending}>
              保存
            </Button>
          )
        }
      >
        代码仓库
      </Heading>
      <RepoList value={draft} onChange={setDraft} currentProject={project.id} />
    </div>
  );
}

function NodesList() {
  const nodes = useNodes();
  const list = nodes.data ?? [];
  return (
    <div className="flex flex-col">
      <Heading
        action={
          <Link to="/settings/nodes" className="text-[13px] text-muted-foreground hover:text-foreground">
            管理节点
          </Link>
        }
      >
        节点
      </Heading>
      <p className="-mt-1 pb-1 text-[13px] text-muted-foreground">员工在这些节点上运行，按岗位模板选择节点。</p>
      {nodes.isLoading && <Loading />}
      {nodes.data && list.length === 0 && <p className="text-[13px] text-muted-foreground">还没有节点。</p>}
      {list.map((n) => (
        <Row key={n.id} title={<span className="flex items-center gap-2">
            <Dot color={n.online ? "#2E7D4F" : "#B7B3AA"} />
            {n.name}
          </span>} desc={`隔离：${n.isolation}`}>
          <span className="text-[13px] tabular-nums text-muted-foreground">
            {n.online ? `会话 ${n.active_sessions} / ${nodeSlots(n)}` : "离线"}
          </span>
        </Row>
      ))}
    </div>
  );
}

// ── 使用情况 ──────────────────────────────────────────────────────────────

function UsageSection({ pid }: { pid: string }) {
  const [days, setDays] = useState(7);
  const usage = useProjectUsage(pid, days);
  const u = usage.data;
  const toggle = (
    <span role="group" aria-label="时间范围" className="flex gap-0.5 rounded-lg bg-raised p-0.5 text-[13px]">
      {[7, 30].map((d) => (
        <button key={d} type="button" aria-pressed={days === d} onClick={() => setDays(d)} className={cn("h-7 rounded-md px-2.5", days === d ? "bg-panel shadow-sm" : "text-muted-foreground")}>
          最近 {d} 天
        </button>
      ))}
    </span>
  );
  return (
    <div className="flex flex-col gap-3">
      <Heading action={toggle}>使用情况</Heading>
      {usage.isLoading && <Loading />}
      {u && u.turns === 0 && <p className="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">最近 {days} 天没有用量记录。</p>}
      {u && u.turns > 0 && (
        <>
          <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
            <Stat label="费用（估算）" value={formatUSD(u.cost_usd_est)} />
            <Stat label="输入 tokens" value={formatTokens(u.tokens.input + u.tokens.cache_read + u.tokens.cache_write)} />
            <Stat label="输出 tokens" value={formatTokens(u.tokens.output)} />
            <Stat label="回合" value={String(u.turns)} />
          </div>
          <DayBars byDay={u.by_day ?? []} days={days} />
          <UsageTable title="按讨论串" rows={(u.by_thread ?? []).map((t) => ({ key: String(t.thread_no ?? "c"), name: t.thread_no ? `#${t.thread_no} ${t.title}` : "项目统筹", cost: t.cost_usd, tokens: t.tokens }))} />
          <UsageTable title="按员工" rows={(u.by_employee ?? []).map((e) => ({ key: `${e.thread_no ?? "c"}-${e.employee}`, name: e.thread_no ? `${e.employee} · #${e.thread_no}` : e.employee, cost: e.cost_usd, tokens: e.tokens }))} />
          <p className="text-xs text-muted-foreground">费用来自 Claude 每个回合结束时报告的数字，订阅账号下是按 API 价格的估算。输入含缓存读写。</p>
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border px-3.5 py-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-lg font-semibold tabular-nums">{value}</span>
    </div>
  );
}

function DayBars({ byDay, days }: { byDay: { day: string; cost_usd: number }[]; days: number }) {
  const list = fillDays(byDay, days);
  const max = Math.max(...list.map((d) => d.cost), 0.0001);
  return (
    <div className="rounded-xl border px-3.5 pb-2 pt-3">
      <span className="text-xs text-muted-foreground">每天费用</span>
      <div className="mt-2 flex h-24 items-end gap-1">
        {list.map((d) => (
          <span key={d.day} title={`${d.day} · ${formatUSD(d.cost)}`} className="flex-1 rounded-t-sm bg-[#1F6FD1]/70" style={{ height: `${Math.max(2, (d.cost / max) * 100)}%`, opacity: d.cost ? 1 : 0.25 }} />
        ))}
      </div>
      <div className="mt-1 flex justify-between text-[11px] text-muted-foreground">
        <span>{list[0]?.day.slice(5)}</span>
        <span>{list[list.length - 1]?.day.slice(5)}</span>
      </div>
    </div>
  );
}

type UsageRow = { key: string; name: string; cost: number; tokens: { input: number; output: number; cache_read: number; cache_write: number } };

function UsageTable({ title, rows }: { title: string; rows: UsageRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="flex flex-col">
      <span className="pb-1 text-[13px] font-medium">{title}</span>
      <div className="flex h-8 items-center gap-3 rounded-lg bg-raised/70 px-3 text-xs text-muted-foreground">
        <span className="min-w-0 flex-1">名称</span>
        <span className="w-20 text-right">输入</span>
        <span className="w-20 text-right">输出</span>
        <span className="w-16 text-right">费用</span>
      </div>
      {rows.map((r) => (
        <div key={r.key} className="flex h-9 items-center gap-3 border-b border-border/60 px-3 text-[13px] last:border-0">
          <span className="min-w-0 flex-1 truncate">{r.name}</span>
          <span className="w-20 text-right tabular-nums text-muted-foreground">{formatTokens(r.tokens.input + r.tokens.cache_read + r.tokens.cache_write)}</span>
          <span className="w-20 text-right tabular-nums text-muted-foreground">{formatTokens(r.tokens.output)}</span>
          <span className="w-16 text-right tabular-nums">{formatUSD(r.cost)}</span>
        </div>
      ))}
    </div>
  );
}

// ── 高级 ────────────────────────────────────────────────────────────────

const APPROVAL_ROUTING_DESC: Record<string, string> = {
  lead_first: "成员的权限请求先交给讨论串的组长：放行、拒绝，或拿不准时转给你。组长自己的请求总是问你。",
  user: "成员的每个权限请求都直接出卡片问你。",
};

export const LEAD_TIMEOUTS = [60, 120, 300, 600];

export function leadTimeoutLabel(seconds: number): string {
  return seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`;
}

function AdvancedSection({ project }: { project: ProjectView }) {
  const update = useUpdate(project.id);
  const d = project.delivery;
  return (
    <div className="flex flex-col">
      <Heading>高级</Heading>
      <span className="pb-1 text-[13px] text-muted-foreground">审批与交付</span>
      <SwitchRow title="新讨论串默认“全部放行”" desc="打开后，讨论串里所有成员的操作都不再询问。适合你在旁边盯着的时候。" checked={project.auto_approve} onChange={(v) => update.mutate({ auto_approve: v })} />
      <Row title="审批方式" desc={APPROVAL_ROUTING_DESC[d.approval_routing]}>
        <Select value={d.approval_routing} onValueChange={(v) => update.mutate({ delivery: { approval_routing: v as "lead_first" | "user" } })}>
          <SelectTrigger aria-label="审批方式" className="h-9 w-[236px] border-0 bg-raised">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="lead_first">组长先判断，拿不准再问我</SelectItem>
            <SelectItem value="user">全部问我</SelectItem>
          </SelectContent>
        </Select>
      </Row>
      {d.approval_routing === "lead_first" && (
        <Row title="组长判断时限" desc="组长在这段时间内没有判断，请求就转给你。">
          <Select value={String(d.lead_review_timeout_s)} onValueChange={(v) => update.mutate({ delivery: { lead_review_timeout_s: Number(v) } })}>
            <SelectTrigger aria-label="组长判断时限" className="h-9 w-36 border-0 bg-raised">
              <SelectValue>{leadTimeoutLabel(d.lead_review_timeout_s)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {LEAD_TIMEOUTS.map((s) => (
                <SelectItem key={s} value={String(s)}>
                  {leadTimeoutLabel(s)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Row>
      )}
      <SwitchRow title="成员提问时通知我" desc="员工用“问你”提问、或请求权限时，侧栏和项目首页都会提醒。" checked={d.notify_on_ask} onChange={(v) => update.mutate({ delivery: { notify_on_ask: v } })} />
      <SwitchRow title="讨论串完成后需要我验收" desc="关闭后 PR 一通过检查就自动合并到基准分支。" checked={project.require_acceptance} onChange={(v) => update.mutate({ require_acceptance: v })} />
      <SwitchRow title="允许员工修改 workflow" desc="允许成员改 .github/workflows 下的文件（默认不允许）。" checked={d.allow_workflow_edits} onChange={(v) => update.mutate({ delivery: { allow_workflow_edits: v } })} />
      <Row title="合并方式" desc="讨论串 PR 合并到基准分支时用。">
        <Select value={d.merge_method} onValueChange={(v) => update.mutate({ delivery: { merge_method: v as "squash" | "merge" | "rebase" } })}>
          <SelectTrigger aria-label="合并方式" className="h-9 w-36 border-0 bg-raised">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="squash">squash</SelectItem>
            <SelectItem value="merge">merge commit</SelectItem>
            <SelectItem value="rebase">rebase</SelectItem>
          </SelectContent>
        </Select>
      </Row>
    </div>
  );
}
