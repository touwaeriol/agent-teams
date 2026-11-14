import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { errorText, PageHeader, Spinner } from "@/components/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api, type AdminUser, type PlatformSettings } from "@/lib/api";
import { useMe } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

export function AdminPage() {
  const me = useMe();
  const client = useQueryClient();
  const users = useQuery({ queryKey: ["admin", "users"], queryFn: () => api<AdminUser[]>("/api/admin/users") });
  const settings = useQuery({ queryKey: ["admin", "settings"], queryFn: () => api<PlatformSettings>("/api/admin/settings") });
  const refresh = () => client.invalidateQueries({ queryKey: ["admin"] });

  const saveSettings = useMutation({
    mutationFn: (s: PlatformSettings) => api<PlatformSettings>("/api/admin/settings", { method: "PUT", json: { ...s } }),
    onSuccess: () => {
      refresh();
      toast.success("已保存");
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const update = useMutation({
    mutationFn: ({ id, changes }: { id: string; changes: Record<string, unknown> }) => api(`/api/admin/users/${id}`, { method: "PATCH", json: changes }),
    onSuccess: refresh,
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api(`/api/admin/users/${id}`, { method: "DELETE" }),
    onSuccess: refresh,
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <div className="container flex flex-col gap-5 py-6">
      <PageHeader title="管理" description="平台设置和用户。" />
      {settings.data && (
        <section className="flex flex-wrap items-center gap-6 rounded-xl border bg-panel p-5">
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
        </section>
      )}
      <section className="overflow-x-auto rounded-xl border bg-panel">
        {users.isLoading && <div className="p-5"><Spinner /></div>}
        <table className="w-full min-w-[760px] text-[13px]">
          <thead className="bg-well text-left text-muted-foreground">
            <tr>
              <th className="px-4 py-3 font-medium">邮箱</th>
              <th className="px-4 py-3 font-medium">角色</th>
              <th className="px-4 py-3 font-medium">状态</th>
              <th className="px-4 py-3 font-medium">节点</th>
              <th className="px-4 py-3 font-medium">节点上限</th>
              <th className="px-4 py-3 font-medium">注册</th>
              <th className="px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            {users.data?.map((u) => {
              const self = u.id === me.data?.id;
              return (
                <tr key={u.id} className="border-t">
                  <td className="px-4 py-3">{u.email}{self && <span className="ml-2 text-xs text-muted-foreground">（你）</span>}</td>
                  <td className="px-4 py-3">
                    <Button size="sm" variant="outline" disabled={self} onClick={() => update.mutate({ id: u.id, changes: { role: u.role === "admin" ? "user" : "admin" } })}>
                      {u.role === "admin" ? "管理员" : "成员"}
                    </Button>
                  </td>
                  <td className="px-4 py-3">
                    <label className="flex items-center gap-2">
                      <Switch checked={u.active} disabled={self} onCheckedChange={(v) => update.mutate({ id: u.id, changes: { active: v } })} />
                      <Badge tone={u.active ? "ok" : "bad"}>{u.active ? "启用" : "停用"}</Badge>
                    </label>
                  </td>
                  <td className="px-4 py-3">{u.nodes}</td>
                  <td className="px-4 py-3">
                    <Input
                      type="number"
                      min={0}
                      className="h-8 w-24"
                      placeholder="默认"
                      defaultValue={u.max_nodes ?? ""}
                      onBlur={(e) =>
                        update.mutate({ id: u.id, changes: e.target.value === "" ? { clear_max_nodes: true } : { max_nodes: Number(e.target.value) } })
                      }
                    />
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">{timeAgo(u.created_at)}</td>
                  <td className="px-4 py-3 text-right">
                    {!self && (
                      <Button size="icon" variant="ghost" aria-label={`删除 ${u.email}`} onClick={() => confirm(`删除 ${u.email}？`) && remove.mutate(u.id)}>
                        <Trash2 />
                      </Button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </section>
    </div>
  );
}
