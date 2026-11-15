// Admin board: platform switches (registration, per-user limits), the user
// table with role / active / password reset / delete, GitHub App and audit log.
import { MoreHorizontal } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { admin } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useAdminSettings, useAdminUsers, useAudit, useMe } from "@/api/queries";
import type { AdminSettings, AdminUserView } from "@/api/views";
import { Card, CardTitle } from "@/components/app/bits";
import { BuiltinLibrary } from "@/components/app/PluginLibrary";
import { ConfirmAction, CopyBlock, InfoDialog, Loading } from "@/components/common";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import { NavButton } from "@/components/shell/Shell";
import { timeAgo } from "@/lib/utils";

export function AdminPage() {
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[980px] flex-col gap-5">
        <div className="flex items-center gap-1">
          <NavButton />
          <h1 className="text-2xl font-semibold">管理</h1>
        </div>
        <SettingsRow />
        <UsersTable />
        <BuiltinLibrary />
        <Card className="flex-row flex-wrap items-center justify-between gap-3">
          <span className="flex flex-col gap-0.5">
            <span className="font-semibold">GitHub App</span>
            <span className="text-xs text-muted-foreground">用户“连接 GitHub”时安装的平台 App，部署后创建一次。</span>
          </span>
          <Link to="/setup/github" className="text-[13px] underline-offset-2 hover:underline">
            查看或创建
          </Link>
        </Card>
        <AuditLog />
      </div>
    </main>
  );
}

function NumberSetting({ label, value, onSave }: { label: string; value: number; onSave: (n: number) => void }) {
  const [text, setText] = useState(String(value));
  useEffect(() => setText(String(value)), [value]);
  const commit = () => {
    const n = Number(text);
    if (Number.isInteger(n) && n >= 0 && n !== value) onSave(n);
    else setText(String(value));
  };
  return (
    <label className="flex items-center gap-2.5">
      {label}
      <input value={text} onChange={(e) => setText(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === "Enter" && commit()} inputMode="numeric" className="h-9 w-16 rounded-lg border border-input px-2.5" />
    </label>
  );
}

function SettingsRow() {
  const settings = useAdminSettings();
  const save = useAction((s: AdminSettings) => admin.saveSettings(s), { invalidate: [keys.adminSettings], success: "已保存" });
  const s = settings.data;
  if (!s) return <Loading />;
  const put = (patch: Partial<AdminSettings>) => save.mutate({ ...s, ...patch });
  return (
    <section className="flex flex-wrap items-center gap-6 rounded-[14px] border bg-panel px-5 py-[18px]">
      <label className="flex items-center gap-2.5">
        <Switch checked={s.registration_open} onCheckedChange={(v) => put({ registration_open: v })} />
        开放注册
      </label>
      <NumberSetting label="每人最多节点" value={s.default_max_nodes} onSave={(n) => put({ default_max_nodes: n })} />
      <NumberSetting label="每人最多同时运行的讨论串" value={s.max_running_threads} onSave={(n) => put({ max_running_threads: n })} />
    </section>
  );
}

function UsersTable() {
  const users = useAdminUsers();
  const me = useMe();
  const [temp, setTemp] = useState<{ email: string; password: string } | null>(null);
  return (
    <div className="overflow-x-auto rounded-[14px] border bg-panel">
      <table className="w-full min-w-[720px] border-collapse text-[13px]">
        <thead>
          <tr className="bg-background text-left text-muted-foreground">
            {["账号", "角色", "节点", "进行中任务", "状态", "最近登录", "注册", ""].map((h) => (
              <th key={h} className="px-4 py-3 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {(users.data ?? []).map((u) => (
            <UserRow key={u.id} user={u} self={u.id === me.data?.id} onTemp={(password) => setTemp({ email: u.email, password })} />
          ))}
        </tbody>
      </table>
      {users.isLoading && <Loading />}
      <InfoDialog open={Boolean(temp)} onClose={() => setTemp(null)} title="临时密码" description={`把它交给 ${temp?.email}，登录后必须修改。只显示这一次。`}>
        {temp && <CopyBlock text={temp.password} />}
      </InfoDialog>
    </div>
  );
}

function UserRow({ user, self, onTemp }: { user: AdminUserView; self: boolean; onTemp: (p: string) => void }) {
  const [deleting, setDeleting] = useState(false);
  const invalidate = [keys.adminUsers];
  const update = useAction((body: { role?: "admin" | "user"; active?: boolean }) => admin.updateUser(user.id, body), { invalidate });
  const reset = useAction(() => admin.resetPassword(user.id), { onSuccess: (r) => onTemp(r.temp_password) });
  const remove = useAction(() => admin.removeUser(user.id), { invalidate, success: "账号已删除" });
  const date = new Date(user.created_at).toLocaleDateString("zh-CN", { month: "long", day: "numeric" });
  return (
    <tr className="border-t border-[#F1EFEA]">
      <td className="px-4 py-3">
        {user.email}
        {self ? "（你）" : ""}
        {user.must_change_password && <span className="ml-1.5 text-xs text-[#B4530B]">待改密码</span>}
      </td>
      <td className="px-4 py-3">{user.role === "admin" ? "管理员" : "成员"}</td>
      <td className="px-4 py-3">{user.nodes}</td>
      <td className="px-4 py-3">{user.running_tasks}</td>
      <td className="px-4 py-3" style={{ color: user.active ? "#2E7D4F" : "#B42318" }}>
        {user.active ? "启用" : "停用"}
      </td>
      <td className="px-4 py-3 text-muted-foreground">{user.last_login_at ? timeAgo(user.last_login_at) : "—"}</td>
      <td className="px-4 py-3 text-muted-foreground">{date}</td>
      <td className="px-2 py-3">
        {!self && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button type="button" aria-label={`${user.email} 的操作`} className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-raised">
                <MoreHorizontal className="size-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => update.mutate({ role: user.role === "admin" ? "user" : "admin" })}>{user.role === "admin" ? "设为成员" : "设为管理员"}</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => update.mutate({ active: !user.active })}>{user.active ? "停用" : "启用"}</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => reset.mutate()}>重置密码</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-destructive" onSelect={() => setDeleting(true)}>
                删除账号
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <ConfirmAction open={deleting} onOpenChange={setDeleting} title={`删除 ${user.email}？`} description="会断开他的节点、删除他的项目和记录，不能恢复。" confirmLabel="删除" onConfirm={() => remove.mutate()} />
      </td>
    </tr>
  );
}

function AuditLog() {
  const [open, setOpen] = useState(false);
  const audit = useAudit();
  return (
    <Card className="gap-3">
      <CardTitle action={<button type="button" onClick={() => setOpen(!open)} className="text-[13px] text-muted-foreground hover:text-foreground">{open ? "收起" : "展开"}</button>}>审计日志</CardTitle>
      {open &&
        (audit.data?.items ?? []).map((e) => (
          <div key={e.id} className="flex flex-wrap gap-x-3 gap-y-0.5 border-t border-[#F1EFEA] pt-2 text-[13px]">
            <span className="font-mono text-xs text-muted-foreground">{new Date(e.created_at).toLocaleString("zh-CN")}</span>
            <span>{e.actor}</span>
            <span className="font-mono">{e.action}</span>
            <span className="text-muted-foreground">
              {e.target_type} {e.target_id}
            </span>
          </div>
        ))}
    </Card>
  );
}
