import { KeyRound, Plus, Server } from "lucide-react";
import { useState } from "react";
import {
  ConfirmAction,
  CopyBlock,
  DataTable,
  EmptyState,
  Field,
  FormDialog,
  InfoDialog,
  Loading,
  nodeStatus,
  Page,
  Panel,
  StatCard,
  StatGrid,
  StatusBadge,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, type Enrollment, type NodeInfo } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { keys, useNodes } from "@/lib/queries";
import { cn, timeAgo } from "@/lib/utils";

export function NodesPage() {
  const nodes = useNodes(5_000);
  const [adding, setAdding] = useState(false);
  const [enrollment, setEnrollment] = useState<{ name: string; data: Enrollment } | null>(null);
  const list = nodes.data ?? [];
  const online = list.filter((n) => n.online);
  const active = online.reduce((sum, n) => sum + (n.active?.length ?? 0), 0);
  const slots = online.reduce((sum, n) => sum + (n.max_runs ?? 0), 0);
  const versions = [...new Set(online.map((n) => n.claude?.split(" ")[0]).filter(Boolean))].join("、");

  return (
    <Page
      title="节点"
      description="每台机器跑一个守护进程，主动连到这里（不用开端口）。员工以 Claude Code 会话的形式在节点上运行，用的是那台机器上 Claude Code 的登录。"
      actions={
        <Button onClick={() => setAdding(true)}>
          <Plus /> 添加节点
        </Button>
      }
    >
      <StatGrid>
        <StatCard label="节点" value={`${list.length}（在线 ${online.length}）`} />
        <StatCard label="运行中的会话 / 总槽位" value={`${active} / ${slots}`} />
        <StatCard label="Claude Code 版本" value={versions || "—"} />
      </StatGrid>
      {nodes.isLoading && <Loading />}
      {list.length === 0 && !nodes.isLoading && (
        <EmptyState
          icon={<Server />}
          title="还没有节点"
          description="添加一个节点，复制安装命令到那台机器上执行，几秒后就会显示在线。"
          action={<Button onClick={() => setAdding(true)}>添加节点</Button>}
        />
      )}
      {list.map((n) => (
        <NodeCard key={n.id} node={n} onToken={(data) => setEnrollment({ name: n.name, data })} />
      ))}
      <AddNodeDialog open={adding} onOpenChange={setAdding} onAdded={(name, data) => setEnrollment({ name, data })} />
      <InfoDialog
        open={enrollment !== null}
        onClose={() => setEnrollment(null)}
        className="max-w-2xl"
        title={`在 ${enrollment?.name ?? ""} 上安装守护进程`}
        description="用要运行员工的那个系统用户（已登录 Claude Code）执行下面的命令。令牌只显示这一次，关掉后可以用“重新生成令牌”换新的。"
      >
        {enrollment && <CopyBlock text={enrollment.data.install} />}
        <p className="text-xs leading-5 text-muted-foreground">
          需要 Python 3.10+ 和 systemd 用户服务。员工的工作目录在 <code className="font-mono">~/.agents_team</code>。装好后执行{" "}
          <code className="font-mono">sudo loginctl enable-linger $USER</code>，退出登录后守护进程也会继续运行。
        </p>
      </InfoDialog>
    </Page>
  );
}

function NodeCard({ node, onToken }: { node: NodeInfo; onToken: (data: Enrollment) => void }) {
  const rotate = useAction(() => api<Enrollment>(`/api/nodes/${node.id}/token`, { method: "POST" }), { invalidate: [keys.nodes], onSuccess: onToken });
  const remove = useAction(() => api(`/api/nodes/${node.id}`, { method: "DELETE" }), { invalidate: [keys.nodes], success: `已移除 ${node.name}` });
  const status = nodeStatus(node);
  return (
    <Panel className={cn(!node.online && "opacity-75")}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <h2 className="font-mono text-lg font-semibold">{node.name}</h2>
          <StatusBadge tone={status.tone} label={status.label} dot />
          <span className="text-[13px] text-muted-foreground">
            {node.online ? `${node.host} · 连接于 ${timeAgo(node.since ?? 0)}` : `创建于 ${timeAgo(node.created_at)}，守护进程未连接`}
          </span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => rotate.mutate()} disabled={rotate.isPending}>
            <KeyRound /> 重新生成令牌
          </Button>
          <ConfirmAction
            title={`移除节点 ${node.name}？`}
            description="它的令牌立即失效，守护进程会断开。机器上的文件不会被删除。"
            confirmLabel="移除"
            onConfirm={() => remove.mutate()}
          />
        </div>
      </div>
      {node.online && (
        <DataTable
          rows={[node]}
          rowKey={(n) => n.id}
          columns={[
            { header: "运行时", cell: () => <span className="rounded border border-[#e07a5f] px-2 py-0.5">Claude Code</span> },
            { header: "版本", cell: (n) => <span className="font-mono">{n.claude}</span> },
            { header: "工作目录", cell: (n) => <span className="font-mono text-muted-foreground">{n.root}</span> },
            { header: "允许执行", cell: (n) => <span className="font-mono text-muted-foreground">{n.allow?.join(", ")}</span> },
            { header: "槽位（同时运行）", cell: (n) => `${n.active?.length ?? 0} / ${n.max_runs}` },
          ]}
        />
      )}
    </Panel>
  );
}

function AddNodeDialog({ open, onOpenChange, onAdded }: { open: boolean; onOpenChange: (o: boolean) => void; onAdded: (name: string, data: Enrollment) => void }) {
  const [name, setName] = useState("");
  const add = useAction(() => api<Enrollment & { node: NodeInfo }>("/api/nodes", { method: "POST", json: { name: name.trim() } }), {
    invalidate: [keys.nodes],
    onSuccess: (data) => {
      onOpenChange(false);
      setName("");
      onAdded(data.node.name, data);
    },
  });
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="添加节点"
      description="给机器起个名字，团队配置里用它指定员工在哪儿运行。"
      submitLabel="生成安装命令"
      busy={add.isPending}
      disabled={!name.trim()}
      onSubmit={() => add.mutate()}
    >
      <Field label="节点名" hint="字母、数字、- 和 _，例如 build-1">
        <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} className="font-mono" />
      </Field>
    </FormDialog>
  );
}
