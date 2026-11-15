// Login board (登录 / 注册 tabs), plus the forced password change after an
// admin reset (`must_change_password`).
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { auth } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { keys } from "@/api/queries";
import { Logo, Spinner } from "@/components/common";
import { cn } from "@/lib/utils";

type Mode = "login" | "register";

function Shell({ subtitle, children }: { subtitle: string; children: React.ReactNode }) {
  return (
    <div className="flex min-h-full items-center justify-center px-4 py-12">
      <div className="flex w-full max-w-[380px] flex-col gap-7">
        <div className="flex flex-col items-center gap-3 text-center">
          <Logo className="size-10" />
          <h1 className="text-[26px] font-semibold tracking-tight">Agent Teams</h1>
          <p className="leading-[22px] text-muted-foreground">{subtitle}</p>
        </div>
        {children}
      </div>
    </div>
  );
}

const input = "h-[42px] rounded-lg border border-input bg-white px-3 outline-none focus:ring-2 focus:ring-ring/40";

export function LoginPage() {
  const qc = useQueryClient();
  const [mode, setMode] = useState<Mode>("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const user = await (mode === "login" ? auth.login : auth.register)({ email: email.trim(), password });
      qc.setQueryData(keys.me, user);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Shell subtitle="把需求交给项目统筹，看团队在你的机器上把活干完。">
      <form onSubmit={submit} className="flex flex-col gap-4 rounded-2xl border bg-panel p-6">
        <div role="tablist" aria-label="登录或注册" className="grid grid-cols-2 gap-1 rounded-[10px] bg-sidebar p-1">
          {(["login", "register"] as Mode[]).map((m) => (
            <button
              key={m}
              type="button"
              role="tab"
              aria-selected={mode === m}
              onClick={() => setMode(m)}
              className={cn("h-[34px] rounded-lg", mode === m ? "bg-white font-semibold shadow-[0_1px_2px_rgba(0,0,0,0.08)]" : "")}
            >
              {m === "login" ? "登录" : "注册"}
            </button>
          ))}
        </div>
        <label className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">邮箱</span>
          <input type="email" required autoComplete="email" placeholder="you@example.com" value={email} onChange={(e) => setEmail(e.target.value)} className={input} />
        </label>
        <label className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">密码</span>
          <input
            type="password"
            required
            minLength={mode === "register" ? 8 : undefined}
            autoComplete={mode === "login" ? "current-password" : "new-password"}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={input}
          />
          {mode === "register" && <span className="text-xs text-muted-foreground">至少 8 位。第一个注册的账号自动成为管理员。</span>}
        </label>
        {error && (
          <p role="alert" className="text-[13px] text-destructive">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy} className="flex h-11 items-center justify-center gap-2 rounded-[10px] bg-primary font-semibold text-primary-foreground disabled:opacity-60">
          {busy && <Spinner />}
          {mode === "login" ? "登录" : "注册并登录"}
        </button>
      </form>
    </Shell>
  );
}

/** Shown instead of the app while `must_change_password` is set. */
export function ChangePasswordPage() {
  const qc = useQueryClient();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await auth.changePassword({ current, new: next });
      await qc.invalidateQueries({ queryKey: keys.me });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Shell subtitle="管理员重置了你的密码，请先设一个新密码。">
      <form onSubmit={submit} className="flex flex-col gap-4 rounded-2xl border bg-panel p-6">
        <label className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">临时密码</span>
          <input type="password" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} className={input} />
        </label>
        <label className="flex flex-col gap-1.5">
          <span className="text-[13px] font-medium">新密码</span>
          <input type="password" required minLength={8} autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} className={input} />
        </label>
        {error && <p role="alert" className="text-[13px] text-destructive">{error}</p>}
        <button type="submit" disabled={busy} className="flex h-11 items-center justify-center gap-2 rounded-[10px] bg-primary font-semibold text-primary-foreground disabled:opacity-60">
          {busy && <Spinner />}
          保存新密码
        </button>
      </form>
    </Shell>
  );
}
