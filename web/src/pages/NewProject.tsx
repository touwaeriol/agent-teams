// NewProject board: name, context, repository, work mode, the per-thread team
// from the role templates, and an optional first message for the coordinator.
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { projects } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useConnections, useRoles } from "@/api/queries";
import { Card, CardTitle, ChoiceCards, WORK_MODES } from "@/components/app/bits";
import { RepoFields, repoPayload, type RepoDraft } from "@/components/app/repo";
import { teamChips } from "@/components/app/roles";
import { Field, Spinner } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { NavButton } from "@/components/shell/Shell";

export function NewProjectPage() {
  const navigate = useNavigate();
  const connections = useConnections();
  const roles = useRoles();
  const [name, setName] = useState("");
  const [context, setContext] = useState("");
  const [repo, setRepo] = useState<RepoDraft>({ git_connection_id: "", repo: "", branch: "" });
  const [mode, setMode] = useState<"worktree" | "clone">("worktree");
  const [first, setFirst] = useState("");
  const firstConnection = connections.data?.[0]?.id;
  useEffect(() => {
    if (firstConnection) setRepo((r) => (r.git_connection_id ? r : { ...r, git_connection_id: firstConnection }));
  }, [firstConnection]);
  const create = useAction(
    () => projects.create({ name: name.trim(), context, work_mode: mode, first_message: first.trim() || undefined, ...repoPayload(repo) }),
    { invalidate: [keys.projects], success: (p) => `已创建项目「${p.name}」`, onSuccess: (p) => navigate(`/projects/${p.id}`) },
  );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name.trim()) create.mutate();
  };
  const chips = teamChips(roles.data?.roles ?? []);
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-10">
      <form onSubmit={submit} className="mx-auto flex max-w-[760px] flex-col gap-6">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <h1 className="text-2xl font-semibold">新建项目</h1>
            <p className="text-[13px] leading-[22px] text-muted-foreground">项目是入口：你和项目统筹聊需求，统筹开讨论串，组长带成员在仓库里完成。</p>
          </div>
        </div>
        <Card className="gap-4">
          <Field label="项目名称">
            <Input required value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：agent team" />
          </Field>
          <Field label="项目上下文" hint="统筹、组长和每个成员开工前都会读到；项目记忆会在讨论串结束后自动补充。">
            <Textarea rows={4} value={context} onChange={(e) => setContext(e.target.value)} placeholder="技术栈、约定、怎么跑测试、不能动的地方……" />
          </Field>
        </Card>
        <Card className="gap-4">
          <CardTitle>代码仓库</CardTitle>
          <RepoFields draft={repo} onChange={setRepo} />
        </Card>
        <Card className="gap-3.5">
          <CardTitle>工作方式</CardTitle>
          <ChoiceCards label="工作方式" value={mode} onChange={setMode} options={WORK_MODES} />
        </Card>
        <Card className="gap-3">
          <CardTitle action={<Link to="/settings/team" className="text-[13px] underline-offset-2 hover:underline">编辑岗位模板</Link>}>每个讨论串的团队</CardTitle>
          <p className="text-[13px] text-muted-foreground">每开一个讨论串，就按这里的岗位拉一组成员；节点槽位不够时排队。</p>
          <div className="flex flex-wrap gap-2">
            {chips.map((r) => (
              <span key={r.name} className="flex h-[34px] items-center gap-2 rounded-lg border bg-background px-3 text-[13px]">
                {r.name}
                <span className="text-muted-foreground">× {r.n}</span>
                <span className="text-[11px] text-muted-foreground">{r.kind}</span>
              </span>
            ))}
          </div>
        </Card>
        <Field label="第一件事（可选，直接发给项目统筹）">
          <Textarea rows={3} value={first} onChange={(e) => setFirst(e.target.value)} placeholder="例如：给项目加上置顶、归档、删除" className="bg-white" />
        </Field>
        <div className="flex justify-end gap-2">
          <Button asChild variant="outline" size="lg">
            <Link to="/projects">取消</Link>
          </Button>
          <Button type="submit" size="lg" disabled={!name.trim() || create.isPending}>
            {create.isPending && <Spinner />} 创建项目
          </Button>
        </div>
      </form>
    </main>
  );
}
