// Nodes board: totals, then one card per machine with slots, runtimes and the
// sessions running there. Adding a node shows its one-time install command.
import { useState } from "react";
import { nodes as nodeActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useNodes, useNodeSessions } from "@/api/queries";
import type { Enrollment, NodeView } from "@/api/views";
import { Bar, StatusPill } from "@/components/app/bits";
import { ConfirmAction, CopyBlock, EmptyState, Field, FormDialog, InfoDialog, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tag-input";
import { NavButton } from "@/components/shell/Shell";
import { nodeSlots, summarizeNodes } from "@/components/shell/nodes";
import { timeAgo } from "@/lib/utils";

interface RuntimeInfo {
  cli?: string;
  sdk?: string;
  auth?: string;
}

const RUNTIMES: [string, string][] = [
  ["claude", "Claude Code"],
  ["codex", "Codex"],
];

/** One runtime chip: version and login state (protocol.md hello.runtimes). */
export function runtimeLine(key: string, info: RuntimeInfo | undefined): { ver: string; login: string; ok: boolean } {
  if (!info || !info.cli) return { ver: "未安装", login: key === "codex" ? "安装后登录或设置 CODEX_API_KEY" : "安装后登录", ok: false };
  if (!info.auth || info.auth === "none") return { ver: info.cli, login: "未登录", ok: false };
  return { ver: info.cli, login: info.auth === "api_key" ? "已配置 API Key" : "已登录", ok: true };
}

const ISOLATION: Record<string, string> = { agent_user: "独立系统用户", sandbox: "沙箱", none: "未隔离" };

export function NodesPage() {
  const nodes = useNodes();
  const [adding, setAdding] = useState(false);
  const [enrollment, setEnrollment] = useState<{ title: string; data: Enrollment } | null>(null);
  const list = nodes.data ?? [];
  const s = summarizeNodes(list);
  const stats = [
    { k: "节点", v: `${s.total}（在线 ${s.online}）` },
    { k: "运行中 / 总槽位", v: `${s.active} / ${s.slots}` },
    { k: "排队中", v: String(s.queued) },
    { k: "停放的会话", v: String(list.reduce((n, x) => n + x.parked_sessions, 0)) },
  ];
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[1040px] flex-col gap-5">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex items-start gap-1">
            <NavButton />
            <div className="flex flex-col gap-1.5">
              <h1 className="text-2xl font-semibold">节点</h1>
              <p className="text-[13px] text-muted-foreground">每台机器跑一个守护进程主动连上来。统筹、组长和成员都运行在这里，用的是机器上 Claude Code / Codex 的登录。</p>
            </div>
          </div>
          <Button className="h-[38px]" onClick={() => setAdding(true)}>
            添加节点
          </Button>
        </header>
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          {stats.map((x) => (
            <div key={x.k} className="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
              <span className="text-xs text-muted-foreground">{x.k}</span>
              <span className="text-xl font-semibold">{x.v}</span>
            </div>
          ))}
        </div>
        {nodes.isLoading && <Loading />}
        {nodes.data && list.length === 0 && <EmptyState title="还没有节点" description="点“添加节点”，在你的机器上运行一行安装命令，守护进程会连上来。" />}
        {list.map((n) => (
          <NodeCard key={n.id} node={n} onToken={(data) => setEnrollment({ title: `${n.name} 的新令牌`, data })} />
        ))}
      </div>
      <AddNode open={adding} onOpenChange={setAdding} onCreated={(name, data) => setEnrollment({ title: `节点 ${name} 已创建`, data })} />
      <InfoDialog open={Boolean(enrollment)} onClose={() => setEnrollment(null)} title={enrollment?.title ?? ""} description="在节点机器上运行下面的命令。令牌只显示这一次。">
        {enrollment && <CopyBlock text={enrollment.data.install} />}
      </InfoDialog>
    </main>
  );
}

function NodeCard({ node, onToken }: { node: NodeView; onToken: (e: Enrollment) => void }) {
  const [configuring, setConfiguring] = useState(false);
  const slots = nodeSlots(node);
  const pct = slots ? `${Math.min(100, Math.round((node.active_sessions / slots) * 100))}%` : "0%";
  const rotate = useAction(() => nodeActions.rotate(node.id), { invalidate: [keys.nodes], onSuccess: onToken });
  const remove = useAction(() => nodeActions.remove(node.id), { invalidate: [keys.nodes], success: "节点已移除" });
  const upgrade = useAction(() => nodeActions.upgrade(node.id), { success: "已让节点升级" });
  const runtimes = (node.runtimes ?? {}) as Record<string, RuntimeInfo>;
  const meta = [node.version ? `v${node.version}` : null, ISOLATION[node.isolation], node.online && node.since ? `连接于 ${timeAgo(node.since)}` : node.last_seen_at ? `最后在线 ${timeAgo(node.last_seen_at)}` : "从未连接"]
    .filter(Boolean)
    .join(" · ");
  return (
    <article className="flex flex-col gap-3.5 rounded-[14px] border bg-panel px-5 py-[18px]">
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <span className="flex flex-wrap items-center gap-2.5">
          <span className="font-mono text-[17px] font-semibold">{node.name}</span>
          <StatusPill tone={node.online ? "#2E7D4F" : "#8A877F"} label={node.online ? "在线" : "离线"} />
          <span className="text-[13px] text-muted-foreground">{meta}</span>
        </span>
        <span className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => setConfiguring(true)}>
            设置
          </Button>
          {node.online && (
            <Button variant="outline" size="sm" onClick={() => upgrade.mutate()} disabled={upgrade.isPending}>
              升级
            </Button>
          )}
          <ConfirmAction title="重新生成令牌？" description="旧令牌立即失效，节点会断开，需要用新命令重新安装。" confirmLabel="重新生成" variant="outline" size="sm" onConfirm={() => rotate.mutate()}>
            重新生成令牌
          </ConfirmAction>
          <ConfirmAction title={`移除节点 ${node.name}？`} description="会断开连接；在它上面的员工改派到其他节点继续。" confirmLabel="移除" size="sm" onConfirm={() => remove.mutate()}>
            移除
          </ConfirmAction>
        </span>
      </div>
      <div className="flex flex-col gap-1.5">
        <span className="flex justify-between text-xs text-muted-foreground">
          <span>槽位（同时运行的会话）</span>
          <span>
            {node.active_sessions} / {slots || "—"}
            {node.queued ? ` · 排队 ${node.queued}` : ""}
          </span>
        </span>
        <Bar pct={pct} tone="#2F6FDB" height={6} />
      </div>
      <div className="flex flex-wrap gap-2 text-[13px]">
        {RUNTIMES.map(([key, label]) => {
          const r = runtimeLine(key, runtimes[key]);
          return (
            <span key={key} className="flex items-center gap-2 rounded-lg border border-[#EFEDE8] bg-background px-2.5 py-1.5">
              <span className="font-medium">{label}</span>
              <span className="font-mono text-xs text-[#4A4843]">{r.ver}</span>
              <span className="text-xs" style={{ color: r.ok ? "#2E7D4F" : "#B4530B" }}>
                {r.login}
              </span>
            </span>
          );
        })}
      </div>
      {node.online && <Sessions node={node} />}
      <NodeSettings node={node} open={configuring} onOpenChange={setConfiguring} />
    </article>
  );
}

function Sessions({ node }: { node: NodeView }) {
  const sessions = useNodeSessions(node.id);
  const list = sessions.data ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-xs text-muted-foreground">正在运行</span>
      {list.length === 0 && <span className="text-[13px] text-muted-foreground">没有会话</span>}
      {list.map((s) => (
        <span key={s.session_id} className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 rounded-lg bg-background px-2.5 py-2 text-[13px]">
          <span className="font-mono">{s.employee}</span>
          <span className="text-muted-foreground">
            {s.thread_no ? `#${s.thread_no} · ` : ""}
            {s.project}
          </span>
          <span className="ml-auto font-mono text-xs text-muted-foreground">{s.state}</span>
        </span>
      ))}
    </div>
  );
}

function AddNode({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (name: string, e: Enrollment) => void }) {
  const [name, setName] = useState("");
  const create = useAction(() => nodeActions.create(name.trim()), {
    invalidate: [keys.nodes],
    onSuccess: (r) => {
      onOpenChange(false);
      setName("");
      onCreated(r.node.name, { token: r.token, install: r.install });
    },
  });
  return (
    <FormDialog open={open} onOpenChange={onOpenChange} title="添加节点" description="起个名字，下一步给你一行安装命令。" submitLabel="创建" busy={create.isPending} disabled={!name.trim()} onSubmit={() => create.mutate()}>
      <Field label="节点名称" hint="字母、数字、- 和 _，例如 rc-erio">
        <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus className="font-mono" />
      </Field>
    </FormDialog>
  );
}

function NodeSettings({ node, open, onOpenChange }: { node: NodeView; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [max, setMax] = useState(node.max_active_sessions?.toString() ?? "");
  const [park, setPark] = useState(node.park_after_s?.toString() ?? "");
  const [isolated, setIsolated] = useState(node.isolated);
  const [labels, setLabels] = useState(node.labels ?? []);
  const save = useAction(
    () =>
      nodeActions.update(node.id, {
        ...(max ? { max_active_sessions: Number(max) } : { clear_max_active_sessions: true }),
        ...(park ? { park_after_s: Number(park) } : { clear_park_after_s: true }),
        isolated,
        labels,
      }),
    { invalidate: [keys.nodes], success: "节点设置已下发", onSuccess: () => onOpenChange(false) },
  );
  return (
    <FormDialog open={open} onOpenChange={onOpenChange} title={`${node.name} 的设置`} submitLabel="保存" busy={save.isPending} onSubmit={() => save.mutate()}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="最多同时运行的会话" hint="留空用守护进程上报的值">
          <Input type="number" min={1} value={max} onChange={(e) => setMax(e.target.value)} />
        </Field>
        <Field label="空闲多久后停放（秒）" hint="留空用默认 900">
          <Input type="number" min={60} value={park} onChange={(e) => setPark(e.target.value)} />
        </Field>
      </div>
      <Field label="标签">
        <TagInput values={labels} onChange={setLabels} placeholder="gpu、big-disk" />
      </Field>
      <label className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 text-[13px]">
        <span className="flex flex-col gap-0.5">
          <span className="font-medium">这台机器是隔离的</span>
          <span className="text-xs text-muted-foreground">整台机器只给员工用、没有其他秘密时打开；Codex 岗位只派到隔离的节点。</span>
        </span>
        <Switch checked={isolated} onCheckedChange={setIsolated} />
      </label>
    </FormDialog>
  );
}
