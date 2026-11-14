import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { errorText, Field, Logo } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, type User } from "@/lib/api";
import { keys } from "@/lib/queries";

export function LoginPage() {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const client = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const me = await api<User>(`/auth/${mode}`, { method: "POST", json: { email, password } });
      client.setQueryData(keys.me, me);
      const from = (location.state as { from?: string } | null)?.from;
      navigate(from && from !== "/login" ? from : "/projects", { replace: true });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-full items-center justify-center px-4 py-12">
      <div className="w-full max-w-[400px]">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <Logo className="size-10" />
          <h1 className="text-2xl font-semibold tracking-tight">Agent Teams</h1>
          <p className="text-[13px] text-muted-foreground">把需求交给负责人，看团队在你的机器上把活干完。</p>
        </div>
        <form onSubmit={submit} className="flex flex-col gap-5 rounded-xl border bg-panel p-6">
          <Tabs value={mode} onValueChange={(v) => setMode(v as "login" | "register")}>
            <TabsList className="grid w-full grid-cols-2">
              <TabsTrigger value="login">登录</TabsTrigger>
              <TabsTrigger value="register">注册</TabsTrigger>
            </TabsList>
          </Tabs>
          <Field label="邮箱" htmlFor="email">
            <Input id="email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field label="密码" htmlFor="password" hint={mode === "register" ? "至少 8 位。第一个注册的账号自动成为管理员。" : undefined}>
            <Input
              id="password"
              type="password"
              autoComplete={mode === "login" ? "current-password" : "new-password"}
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          {error && <p className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-[13px] text-destructive">{error}</p>}
          <Button type="submit" size="lg" disabled={busy}>
            {mode === "login" ? "登录" : "注册并登录"}
          </Button>
        </form>
      </div>
    </div>
  );
}
