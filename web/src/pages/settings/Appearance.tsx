// 外观 (theme, this browser only) plus the account basics v2 adds:
// change password and the list of signed-in sessions.
import { Moon, Sun } from "lucide-react";
import { useState } from "react";
import { auth } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useSessions } from "@/api/queries";
import { Card, CardTitle } from "@/components/app/bits";
import { Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NavButton } from "@/components/shell/Shell";
import { useTheme, type Theme } from "@/lib/theme";
import { cn, timeAgo } from "@/lib/utils";

const OPTIONS: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "亮色", icon: Sun },
  { value: "dark", label: "暗色", icon: Moon },
];

export function AppearancePage() {
  const { theme, setTheme } = useTheme();
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[760px] flex-col gap-5">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <h1 className="text-2xl font-semibold">外观与账号</h1>
            <p className="text-[13px] text-muted-foreground">主题只影响这台设备上的浏览器。</p>
          </div>
        </div>
        <Card className="gap-3.5">
          <CardTitle>主题</CardTitle>
          <div className="grid grid-cols-2 gap-3">
            {OPTIONS.map((o) => (
              <button
                key={o.value}
                type="button"
                onClick={() => setTheme(o.value)}
                aria-pressed={theme === o.value}
                className={cn("flex items-center gap-3 rounded-xl border-[1.5px] p-4 text-left", theme === o.value ? "border-foreground" : "border-border hover:border-input")}
              >
                <o.icon className="size-5 text-muted-foreground" />
                <span className="font-medium">{o.label}</span>
              </button>
            ))}
          </div>
        </Card>
        <PasswordCard />
        <SessionsCard />
      </div>
    </main>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const change = useAction(() => auth.changePassword({ current, new: next }), {
    invalidate: [keys.sessions],
    success: "密码已修改，其他设备上的登录已退出",
    onSuccess: () => (setCurrent(""), setNext("")),
  });
  return (
    <Card className="gap-3.5">
      <CardTitle>修改密码</CardTitle>
      <form
        className="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          change.mutate();
        }}
      >
        <Field label="当前密码">
          <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        </Field>
        <Field label="新密码（至少 8 位）">
          <Input type="password" autoComplete="new-password" minLength={8} value={next} onChange={(e) => setNext(e.target.value)} />
        </Field>
        <Button type="submit" disabled={!current || next.length < 8 || change.isPending}>
          保存
        </Button>
      </form>
    </Card>
  );
}

function SessionsCard() {
  const sessions = useSessions();
  const revoke = useAction((id: string) => auth.revokeSession(id), { invalidate: [keys.sessions], success: "已退出该设备" });
  return (
    <Card className="gap-3">
      <CardTitle>登录的设备</CardTitle>
      {(sessions.data ?? []).map((s) => (
        <div key={s.id} className="flex items-center gap-3 rounded-lg bg-background px-3 py-2.5 text-[13px]">
          <span className="min-w-0 flex-1">
            <span className="block truncate">{s.user_agent || "未知浏览器"}</span>
            <span className="text-xs text-muted-foreground">
              {s.current ? "这台设备 · " : ""}最近活动 {timeAgo(s.last_seen_at ?? s.created_at)}
            </span>
          </span>
          {!s.current && (
            <Button variant="outline" size="sm" onClick={() => revoke.mutate(s.id)}>
              退出
            </Button>
          )}
        </div>
      ))}
    </Card>
  );
}
