import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Server, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { CopyBlock, EmptyState, errorText, Field, PageHeader, Spinner } from "@/components/shared";
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
import { Badge, Dot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api, type Enrollment, type NodeInfo } from "@/lib/api";
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

  return (
    <div className="container flex flex-col gap-5 py-6">
      <PageHeader
        title="节点"
        description="每台机器跑一个守护进程，主动连到这里（不用开端口）。员工以 Claude Code 会话的形式在节点上运行，用的是那台机器上 Claude Code 的登录。"
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus /> 添加节点
          </Button>
        }
      />
      <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3">
        <Stat label="节点" value={`${list.length}（在线 ${online.length}）`} />
        <Stat label="运行中的会话 / 总槽位" value={`${active} / ${slots}`} />
        <Stat label="Claude Code 版本" value={[...new Set(online.map((n) => n.claude?.split(" ")[0]).filter(Boolean))].join("、") || "—"} />
      </div>
      {nodes.isLoading && <Spinner />}
      {list.length === 0 && !nodes.isLoading && (
        <EmptyState icon={<Server />} title="还没有节点" description="添加一个节点，复制安装命令到那台机器上执行，几秒后就会显示在线。" action={<Button onClick={() => setAdding(true)}>添加节点</Button>} />
      )}
      {list.map((n) => (
        <NodeCard key={n.id} node={n} onToken={(data) => setEnrollment({ name: n.name, data })} />
      ))}
      <AddNodeDialog open={adding} onOpenChange={setAdding} onAdded={(name, data) => setEnrollment({ name, data })} />
      <Dialog open={enrollment !== null} onOpenChange={(open) => !open && setEnrollment(null)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>在 {enrollment?.name} 上安装守护进程</DialogTitle>
            <DialogDescription>
              用要运行员工的那个系统用户（已登录 Claude Code）执行下面的命令。令牌只显示这一次，关掉后可以用“重新生成令牌”换新的。
            </DialogDescription>
          </DialogHeader>
          {enrollment && <CopyBlock text={enrollment.data.install} />}
          <p className="text-xs leading-5 text-muted-foreground">
            需要 Python 3.10+ 和 systemd 用户服务。员工的工作目录在 <code className="font-mono">~/.agents_team</code>。装好后执行{" "}
            <code className="font-mono">sudo loginctl enable-linger $USER</code>，退出登录后守护进程也会继续运行。
          </p>
          <DialogFooter>
            <Button onClick={() => setEnrollment(null)}>完成</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="truncate text-xl font-semibold">{value}</span>
    </div>
  );
}

function NodeCard({ node, onToken }: { node: NodeInfo; onToken: (data: Enrollment) => void }) {
  const client = useQueryClient();
  const rotate = useMutation({
    mutationFn: () => api<Enrollment>(`/api/nodes/${node.id}/token`, { method: "POST" }),
    onSuccess: (data) => {
      client.invalidateQueries({ queryKey: keys.nodes });
      onToken(data);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: () => api(`/api/nodes/${node.id}`, { method: "DELETE" }),
    onSuccess: () => {
      client.invalidateQueries({ queryKey: keys.nodes });
      toast.success(`已移除 ${node.name}`);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const busy = node.active?.length ?? 0;
  return (
    <section className={cn("flex flex-col gap-4 rounded-xl border bg-panel p-5", !node.online && "opacity-75")}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <h2 className="font-mono text-lg font-semibold">{node.name}</h2>
          <Badge tone={node.online ? (busy >= (node.max_runs ?? 99) ? "warn" : "ok") : "bad"}>
            <Dot tone={node.online ? "ok" : "bad"} />
            {node.online ? (busy >= (node.max_runs ?? 99) ? "在线 · 槽位已满" : "在线") : "离线"}
          </Badge>
          <span className="text-[13px] text-muted-foreground">
            {node.online ? `${node.host} · 连接于 ${timeAgo(node.since ?? 0)}` : `创建于 ${timeAgo(node.created_at)}，守护进程未连接`}
          </span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => rotate.mutate()} disabled={rotate.isPending}>
            <KeyRound /> 重新生成令牌
          </Button>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="destructive">
                <Trash2 /> 移除
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>移除节点 {node.name}？</AlertDialogTitle>
                <AlertDialogDescription>它的令牌立即失效，守护进程会断开。机器上的文件不会被删除。</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>取消</AlertDialogCancel>
                <AlertDialogAction onClick={() => remove.mutate()}>移除</AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>
      {node.online && (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full min-w-[640px] border-collapse text-[13px]">
            <thead className="bg-well text-left text-muted-foreground">
              <tr>
                <th className="px-3 py-2.5 font-medium">运行时</th>
                <th className="px-3 py-2.5 font-medium">版本</th>
                <th className="px-3 py-2.5 font-medium">工作目录</th>
                <th className="px-3 py-2.5 font-medium">允许执行</th>
                <th className="px-3 py-2.5 font-medium">槽位（同时运行）</th>
              </tr>
            </thead>
            <tbody>
              <tr className="border-t">
                <td className="px-3 py-2.5">
                  <span className="rounded border border-[#e07a5f] px-2 py-0.5">Claude Code</span>
                </td>
                <td className="px-3 py-2.5 font-mono">{node.claude}</td>
                <td className="px-3 py-2.5 font-mono text-muted-foreground">{node.root}</td>
                <td className="px-3 py-2.5 font-mono text-muted-foreground">{node.allow?.join(", ")}</td>
                <td className="px-3 py-2.5">
                  {busy} / {node.max_runs}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function AddNodeDialog({ open, onOpenChange, onAdded }: { open: boolean; onOpenChange: (o: boolean) => void; onAdded: (name: string, data: Enrollment) => void }) {
  const [name, setName] = useState("");
  const client = useQueryClient();
  const add = useMutation({
    mutationFn: () => api<Enrollment & { node: NodeInfo }>("/api/nodes", { method: "POST", json: { name: name.trim() } }),
    onSuccess: (data) => {
      client.invalidateQueries({ queryKey: keys.nodes });
      onOpenChange(false);
      setName("");
      onAdded(data.node.name, data);
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
            <DialogTitle>添加节点</DialogTitle>
            <DialogDescription>给机器起个名字，团队配置里用它指定员工在哪儿运行。</DialogDescription>
          </DialogHeader>
          <Field label="节点名" hint="字母、数字、- 和 _，例如 build-1">
            <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} className="font-mono" />
          </Field>
          <DialogFooter>
            <Button type="submit" disabled={!name.trim() || add.isPending}>
              {add.isPending && <Spinner />} 生成安装命令
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
