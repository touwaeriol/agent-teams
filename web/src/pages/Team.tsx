import { Crown, Plus, Save, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import YAML from "yaml";
import { Callout, ConfirmAction, Disclosure, errorText, Field, Loading, MEMBER_TYPES, nodeStatus, Page, Panel, Spinner, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tag-input";
import { Textarea } from "@/components/ui/textarea";
import { api, type Employee, type NodeInfo, type TeamDefinition, type TeamState } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { keys, useNodes, useTeam } from "@/lib/queries";
import { canLead, memberType, switchMemberType, type MemberType } from "@/lib/team";

const PERMISSION_MODES = [
  { value: "acceptEdits", label: "自动接受改文件（默认）" },
  { value: "plan", label: "只读规划（不改代码）" },
  { value: "bypassPermissions", label: "跳过所有权限确认" },
  { value: "default", label: "逐项询问（在对话里弹审批卡片）" },
];

const SANDBOXES = [
  { value: "workspace-write", label: "可写工作目录（默认）" },
  { value: "read-only", label: "只读" },
  { value: "danger-full-access", label: "不限制（危险）" },
];

const PRESETS: Record<string, Omit<Employee, "name" | "node">> = {
  工程师: {
    type: "claude_code",
    description: "工程师：写代码、跑测试，改动放在新分支并推送。",
    instructions: "你是工程师。按任务改代码并跑相关测试，改动提交到新分支并推送，最后用中文几句话说明改了什么、测试结果如何。",
    allowed_tools: ["Bash(git *)", "Bash(gh *)", "Bash(npm *)", "Bash(pnpm *)", "Bash(python3 *)", "Bash(pytest *)"],
    max_turns: 40,
  },
  审查员: {
    type: "claude_code",
    description: "审查员（只读）：审查改动、指出问题，不改代码。",
    permission_mode: "plan",
    allowed_tools: ["Bash(git diff *)", "Bash(git log *)", "Bash(git show *)", "Bash(gh pr view *)", "Bash(gh pr diff *)"],
  },
  测试: {
    type: "claude_code",
    description: "测试：补测试用例、跑全量测试、复现问题。",
    instructions: "你负责测试。补齐用例、运行测试并如实报告失败。",
    allowed_tools: ["Bash(git *)", "Bash(npm *)", "Bash(pnpm *)", "Bash(pytest *)", "Bash(python3 *)"],
  },
  "Codex 工程师": {
    type: "codex",
    description: "Codex 工程师：用 OpenAI Codex 写代码、跑测试。",
    instructions: "你是工程师。按任务改代码并跑相关测试，最后用中文几句话说明改了什么、测试结果如何。",
  },
};

const PRESET_NAMES: Record<string, string> = { 审查员: "reviewer", 测试: "qa", "Codex 工程师": "codex" };

export function TeamPage() {
  const team = useTeam();
  const nodes = useNodes();
  const [draft, setDraft] = useState<TeamDefinition | null>(null);
  const [yamlText, setYamlText] = useState("");
  const [tab, setTab] = useState("form");
  const [yamlError, setYamlError] = useState("");

  useEffect(() => {
    if (team.data && draft === null) {
      setDraft(team.data.team ?? { leader: "", employees: [] });
      setYamlText(team.data.config);
    }
  }, [team.data, draft]);

  const save = useAction(() => api<TeamState>("/api/team", { method: "PUT", json: tab === "yaml" ? { config: yamlText } : { team: clean(draft!) } }), {
    invalidate: [keys.team],
    success: "团队已保存，新对话和下一条消息起生效",
    onSuccess: (state) => {
      setDraft(state.team ?? draft);
      setYamlText(state.config);
    },
  });

  if (!team.data || !draft) return <Loading />;

  const switchTab = (next: string) => {
    if (next === "yaml") {
      setYamlText(YAML.stringify(clean(draft), { lineWidth: 0 }));
    } else {
      try {
        const parsed = YAML.parse(yamlText) as TeamDefinition;
        setDraft(parsed ?? { employees: [] });
        setYamlError("");
      } catch (e) {
        setYamlError(errorText(e));
        return;
      }
    }
    setTab(next);
  };

  const employees = draft.employees ?? [];
  const setEmployees = (list: Employee[]) => setDraft({ ...draft, employees: list });
  const nodeNames = (nodes.data ?? []).map((n) => n.name);
  const add = (preset: string) => {
    const base = PRESETS[preset];
    const name = uniqueName(PRESET_NAMES[preset] ?? "dev", employees);
    setEmployees([...employees, { name, node: nodeNames[0] ?? "", ...base }]);
  };

  return (
    <Page
      title="团队"
      description="负责人和员工运行在你的节点上：负责人是 Claude Code，员工可以是 Claude Code 或 Codex。负责人直接和你对话、派任务；员工各自在节点上干活。所有项目共用这支团队，项目的上下文和仓库会自动交给每个人。"
      actions={
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending ? <Spinner /> : <Save />} 保存团队
        </Button>
      }
    >
      {team.data.error && (
        <Callout tone="warn" icon={<TriangleAlert />}>
          {team.data.saved ? `当前保存的配置有问题：${team.data.error}` : "还没有保存过团队配置，下面是一份示例，改好后保存。"}
        </Callout>
      )}
      {nodeNames.length === 0 && (
        <Callout tone="warn">
          还没有节点。<Link to="/settings/nodes">先添加节点</Link>，员工才有地方运行。
        </Callout>
      )}
      <Tabs value={tab} onValueChange={switchTab}>
        <TabsList>
          <TabsTrigger value="form">表单</TabsTrigger>
          <TabsTrigger value="yaml">YAML</TabsTrigger>
        </TabsList>
        {yamlError && <Callout tone="bad" className="mt-2">YAML 解析失败：{yamlError}</Callout>}
        <TabsContent value="form" className="flex flex-col gap-4">
          <Panel className="flex-row flex-wrap items-end">
            <div className="w-full max-w-xs">
              <Field label="负责人（直接和你对话的人）">
                <Select value={draft.leader ?? ""} onValueChange={(v) => setDraft({ ...draft, leader: v })}>
                  <SelectTrigger>
                    <SelectValue placeholder="选择负责人" />
                  </SelectTrigger>
                  <SelectContent>
                    {employees.filter(canLead).map((e) => (
                      <SelectItem key={e.name} value={e.name}>
                        {e.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <div className="flex flex-wrap gap-2">
              {Object.keys(PRESETS).map((p) => (
                <Button key={p} variant="outline" onClick={() => add(p)}>
                  <Plus /> 添加{p}
                </Button>
              ))}
            </div>
          </Panel>
          <div className="grid gap-4 xl:grid-cols-2">
            {employees.map((e, i) => (
              <EmployeeCard
                key={i}
                employee={e}
                leader={e.name === draft.leader}
                nodes={nodes.data ?? []}
                onChange={(next) => {
                  const list = [...employees];
                  list[i] = next;
                  const leader = draft.leader === e.name ? next.name : draft.leader;
                  setDraft({ ...draft, employees: list, leader });
                }}
                onRemove={() => setEmployees(employees.filter((_, j) => j !== i))}
              />
            ))}
          </div>
        </TabsContent>
        <TabsContent value="yaml">
          <Textarea
            value={yamlText}
            onChange={(e) => setYamlText(e.target.value)}
            spellCheck={false}
            rows={28}
            className="font-mono text-[12px] leading-5"
            aria-label="团队 YAML"
          />
        </TabsContent>
      </Tabs>
    </Page>
  );
}

function EmployeeCard({
  employee,
  leader,
  nodes,
  onChange,
  onRemove,
}: {
  employee: Employee;
  leader: boolean;
  nodes: NodeInfo[];
  onChange: (e: Employee) => void;
  onRemove: () => void;
}) {
  const set = (patch: Partial<Employee>) => onChange({ ...employee, ...patch });
  const status = nodeStatus(nodes.find((n) => n.name === employee.node));
  return (
    <Panel
      as="article"
      tone={leader ? "highlight" : "default"}
      title={
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-mono">{employee.name || "未命名"}</span>
          {leader && <StatusBadge tone="primary" label={<><Crown className="size-3" /> 负责人</>} />}
          <StatusBadge tone={status.tone} dot label={employee.node || "未选节点"} />
          {memberType(employee) === "codex" && <StatusBadge tone="info" label="Codex" />}
          {employee.can_delegate && <StatusBadge tone="neutral" label="可派子任务" />}
        </span>
      }
      action={<ConfirmAction iconOnly={`删除 ${employee.name}`} title={`从团队里删除 ${employee.name}？`} description="保存团队后生效。" onConfirm={onRemove} />}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="名字（英文，派任务时用）">
          <Input value={employee.name} onChange={(e) => set({ name: e.target.value.replace(/\s+/g, "-") })} className="font-mono" />
        </Field>
        <Field label="类型" hint={leader ? "负责人只能是 Claude Code。" : undefined}>
          <Select value={memberType(employee)} onValueChange={(v) => onChange(switchMemberType(employee, v as MemberType))} disabled={leader}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {MEMBER_TYPES.map((t) => (
                <SelectItem key={t.value} value={t.value}>
                  {t.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="运行在哪个节点">
          <Select value={employee.node ?? ""} onValueChange={(v) => set({ node: v })}>
            <SelectTrigger>
              <SelectValue placeholder="选择节点" />
            </SelectTrigger>
            <SelectContent>
              {nodes.map((n) => (
                <SelectItem key={n.id} value={n.name}>
                  {n.name} {n.online ? "· 在线" : "· 离线"}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <Field label="职责（负责人据此决定派给谁）">
        <Input value={employee.description ?? ""} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <Field label="工作要求（追加到系统提示）">
        <Textarea rows={3} value={employee.instructions ?? ""} onChange={(e) => set({ instructions: e.target.value })} />
      </Field>
      {!leader && <DelegateSwitch employee={employee} onChange={(on) => set({ can_delegate: on || undefined })} />}
      {memberType(employee) === "codex" ? <CodexSettings employee={employee} set={set} /> : (
      <Disclosure summary="权限与高级设置">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="权限模式">
              <Select value={employee.permission_mode ?? "acceptEdits"} onValueChange={(v) => set({ permission_mode: v === "acceptEdits" ? undefined : v })}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PERMISSION_MODES.map((m) => (
                    <SelectItem key={m.value} value={m.value}>
                      {m.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="单个任务最多轮数">
              <Input
                type="number"
                min={1}
                placeholder="不限"
                value={employee.max_turns ?? ""}
                onChange={(e) => set({ max_turns: e.target.value ? Number(e.target.value) : undefined })}
              />
            </Field>
          </div>
          <Field label="模型（留空用 Claude Code 默认）">
            <Input value={employee.model ?? ""} onChange={(e) => set({ model: e.target.value || undefined })} placeholder="例如 claude-opus-5-5" className="font-mono" />
          </Field>
          <Field label="允许的工具（免确认）">
            <TagInput values={employee.allowed_tools ?? []} onChange={(v) => set({ allowed_tools: v.length ? v : undefined })} placeholder="Bash(git *)，回车添加" />
          </Field>
          <Field label="禁止的工具">
            <TagInput values={employee.disallowed_tools ?? []} onChange={(v) => set({ disallowed_tools: v.length ? v : undefined })} placeholder="WebFetch，回车添加" />
          </Field>
      </Disclosure>
      )}
    </Panel>
  );
}

function DelegateSwitch({ employee, onChange }: { employee: Employee; onChange: (on: boolean) => void }) {
  const id = `delegate-${employee.name}`;
  return (
    <div className="flex items-start justify-between gap-4 rounded-lg border px-3 py-2.5">
      <label htmlFor={id} className="flex flex-col gap-0.5">
        <span className="text-[13px] font-medium text-foreground/85">可以给其他员工派子任务</span>
        <span className="text-xs leading-5 text-muted-foreground">打开后这个员工能把一部分工作拆给同事，并等待结果。</span>
      </label>
      <Switch id={id} checked={Boolean(employee.can_delegate)} onCheckedChange={onChange} />
    </div>
  );
}

/** Codex has no per-tool permissions; its sandbox decides what it may touch. */
function CodexSettings({ employee, set }: { employee: Employee; set: (patch: Partial<Employee>) => void }) {
  return (
    <Disclosure summary="沙箱与高级设置">
      <Field label="沙箱" hint="Codex 以非交互方式运行，不会弹审批卡片，能做什么由沙箱决定。">
        <Select value={employee.sandbox ?? "workspace-write"} onValueChange={(v) => set({ sandbox: v === "workspace-write" ? undefined : v })}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SANDBOXES.map((m) => (
              <SelectItem key={m.value} value={m.value}>
                {m.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field label="模型（留空用 Codex 默认）">
        <Input value={employee.model ?? ""} onChange={(e) => set({ model: e.target.value || undefined })} className="font-mono" />
      </Field>
    </Disclosure>
  );
}

function uniqueName(base: string, employees: Employee[]): string {
  const taken = new Set(employees.map((e) => e.name));
  if (!taken.has(base)) return base;
  let n = 2;
  while (taken.has(`${base}-${n}`)) n += 1;
  return `${base}-${n}`;
}

function clean(team: TeamDefinition): TeamDefinition {
  const employees = (team.employees ?? []).map((e) => {
    const entries = Object.entries({ type: "claude_code", ...e }).filter(([, v]) => v !== undefined && v !== "" && !(Array.isArray(v) && v.length === 0));
    return Object.fromEntries(entries) as Employee;
  });
  return { ...team, employees };
}
