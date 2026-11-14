import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { ConfirmAction, DataTable, Loading, Page, Panel, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api, type AdminUser, type PlatformSettings } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { useMe } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

const ADMIN = ["admin"];

export function AdminPage() {
  const me = useMe();
  const users = useQuery({ queryKey: [...ADMIN, "users"], queryFn: () => api<AdminUser[]>("/api/admin/users") });
  const settings = useQuery({ queryKey: [...ADMIN, "settings"], queryFn: () => api<PlatformSettings>("/api/admin/settings") });

  const saveSettings = useAction((s: PlatformSettings) => api<PlatformSettings>("/api/admin/settings", { method: "PUT", json: { ...s } }), {
    invalidate: [ADMIN],
    success: "已保存",
  });
  const update = useAction(({ id, changes }: { id: string; changes: Record<string, unknown> }) => api(`/api/admin/users/${id}`, { method: "PATCH", json: changes }), {
    invalidate: [ADMIN],
  });
  const remove = useAction((id: string) => api(`/api/admin/users/${id}`, { method: "DELETE" }), { invalidate: [ADMIN], success: "已删除" });

  return (
    <Page
      title="管理"
      description="平台设置和用户。"
      actions={
        <Button asChild variant="outline">
          <Link to="/ui">组件库</Link>
        </Button>
      }
    >
      {settings.data && (
        <Panel title="平台设置">
          <div className="flex flex-wrap items-center gap-6">
            <label className="flex items-center gap-3">
              <Switch checked={settings.data.registration_open} onCheckedChange={(v) => saveSettings.mutate({ ...settings.data!, registration_open: v })} />
              <span>开放注册</span>
            </label>
            <label className="flex items-center gap-3">
              <span>每人默认最多节点数</span>
              <Input
                type="number"
                min={0}
                className="w-24"
                defaultValue={settings.data.default_max_nodes}
                onBlur={(e) => saveSettings.mutate({ ...settings.data!, default_max_nodes: Number(e.target.value) })}
              />
            </label>
          </div>
        </Panel>
      )}
      {users.isLoading ? (
        <Loading />
      ) : (
        <DataTable
          className="bg-panel"
          minWidth={760}
          rows={users.data ?? []}
          rowKey={(u) => u.id}
          columns={[
            {
              header: "邮箱",
              cell: (u) => (
                <>
                  {u.email}
                  {u.id === me.data?.id && <span className="ml-2 text-xs text-muted-foreground">（你）</span>}
                </>
              ),
            },
            {
              header: "角色",
              cell: (u) => (
                <Button size="sm" variant="outline" disabled={u.id === me.data?.id} onClick={() => update.mutate({ id: u.id, changes: { role: u.role === "admin" ? "user" : "admin" } })}>
                  {u.role === "admin" ? "管理员" : "成员"}
                </Button>
              ),
            },
            {
              header: "状态",
              cell: (u) => (
                <label className="flex items-center gap-2">
                  <Switch checked={u.active} disabled={u.id === me.data?.id} onCheckedChange={(v) => update.mutate({ id: u.id, changes: { active: v } })} />
                  <StatusBadge tone={u.active ? "ok" : "bad"} label={u.active ? "启用" : "停用"} />
                </label>
              ),
            },
            { header: "节点", cell: (u) => u.nodes },
            {
              header: "节点上限",
              cell: (u) => (
                <Input
                  type="number"
                  min={0}
                  className="h-8 w-24"
                  placeholder="默认"
                  defaultValue={u.max_nodes ?? ""}
                  onBlur={(e) => update.mutate({ id: u.id, changes: e.target.value === "" ? { clear_max_nodes: true } : { max_nodes: Number(e.target.value) } })}
                />
              ),
            },
            { header: "注册", cell: (u) => <span className="text-muted-foreground">{timeAgo(u.created_at)}</span> },
            {
              header: "",
              className: "text-right",
              cell: (u) =>
                u.id !== me.data?.id && (
                  <ConfirmAction iconOnly={`删除 ${u.email}`} title={`删除用户 ${u.email}？`} description="会同时删除他的节点、团队、项目和对话，无法恢复。" onConfirm={() => remove.mutate(u.id)} />
                ),
            },
          ]}
        />
      )}
    </Page>
  );
}
