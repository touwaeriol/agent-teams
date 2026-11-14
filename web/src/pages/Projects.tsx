import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, FolderGit2, GitBranch, MessageSquare, Plus, Settings } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { draftPayload, emptyDraft, ProjectFields, type ProjectDraft } from "@/components/ProjectFields";
import { EmptyState, errorText, Field, PageHeader, Spinner } from "@/components/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api, type Conversation, type Project } from "@/lib/api";
import { keys, useProjects } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

export function ProjectsPage() {
  const projects = useProjects();
  const [params] = useSearchParams();
  const highlight = params.get("new") === "1";

  return (
    <div className="container flex flex-wrap items-start gap-5 py-6">
      <section className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">
        <PageHeader
          title="项目"
          description="一个项目 = 一个 GitHub 仓库 + 项目上下文 + 和负责人的对话。你只和负责人说话，它负责拆任务、派给员工，员工在节点上各自的仓库克隆里干活。"
        />
        {projects.isLoading && <Spinner />}
        {projects.data?.length === 0 && (
          <EmptyState icon={<FolderGit2 />} title="还没有项目" description="在右边填好名称、上下文，选一个仓库，就能开始和负责人对话。" />
        )}
        {projects.data?.map((p) => <ProjectCard key={p.id} project={p} />)}
      </section>
      <aside className="w-full min-w-0 flex-[1_1_400px] lg:sticky lg:top-24">
        <NewProject highlight={highlight} />
      </aside>
    </div>
  );
}

function ProjectCard({ project }: { project: Project }) {
  return (
    <article className="flex flex-col gap-3 rounded-xl border bg-panel p-5 transition-colors hover:border-input">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Link to={`/projects/${project.id}`} className="text-[17px] font-semibold hover:text-accent-foreground">
          {project.name}
        </Link>
        <span className="text-xs text-muted-foreground">更新于 {timeAgo(project.updated_at)}</span>
      </div>
      {project.description && <p className="text-[13px] text-muted-foreground">{project.description}</p>}
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        {project.repo ? (
          <>
            <a
              href={`https://github.com/${project.repo}`}
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-1.5 rounded-md bg-raised px-2 py-1 font-mono text-xs hover:text-accent-foreground"
            >
              {project.repo}
              <ExternalLink className="size-3" />
            </a>
            <Badge tone="neutral">
              <GitBranch className="size-3" />
              {project.branch || "默认分支"}
            </Badge>
          </>
        ) : (
          <Badge tone="warn">未关联仓库</Badge>
        )}
        {project.context && <Badge tone="info">有项目上下文</Badge>}
      </div>
      <div className="flex flex-wrap gap-2 pt-1">
        <Button asChild>
          <Link to={`/projects/${project.id}`}>
            <MessageSquare /> 打开对话
          </Link>
        </Button>
        <Button asChild variant="outline">
          <Link to={`/projects/${project.id}/settings`}>
            <Settings /> 设置
          </Link>
        </Button>
      </div>
    </article>
  );
}

function NewProject({ highlight }: { highlight: boolean }) {
  const [draft, setDraft] = useState<ProjectDraft>(emptyDraft);
  const [firstMessage, setFirstMessage] = useState("");
  const client = useQueryClient();
  const navigate = useNavigate();

  const create = useMutation({
    mutationFn: async () => {
      const project = await api<Project>("/api/projects", { method: "POST", json: draftPayload(draft) });
      const conversation = await api<Conversation>("/api/conversations", {
        method: "POST",
        json: { title: firstMessage.trim().slice(0, 40) || "第一次对话", project_id: project.id },
      });
      if (firstMessage.trim()) {
        await api(`/api/conversations/${conversation.id}/messages`, { method: "POST", json: { content: firstMessage.trim() } }).catch((e) =>
          toast.error(`项目已创建，但第一条消息没发出去：${errorText(e)}`),
        );
      }
      return { project, conversation };
    },
    onSuccess: ({ project, conversation }) => {
      client.invalidateQueries({ queryKey: keys.projects });
      toast.success(`已创建项目「${project.name}」`);
      navigate(`/projects/${project.id}/c/${conversation.id}`);
    },
    onError: (e) => toast.error(errorText(e)),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    create.mutate();
  };

  return (
    <form onSubmit={submit} className={`flex flex-col gap-5 rounded-xl border bg-panel p-5 ${highlight ? "border-primary/60" : ""}`}>
      <div className="flex items-center gap-2">
        <Plus className="size-4 text-primary" />
        <h2 className="text-[17px] font-semibold">新建项目</h2>
      </div>
      <ProjectFields draft={draft} onChange={setDraft} />
      <Field label="要做什么（可选，会成为对话里的第一条消息）" htmlFor="p-first">
        <Textarea id="p-first" rows={4} placeholder="目标、范围、期限、验收方式" value={firstMessage} onChange={(e) => setFirstMessage(e.target.value)} />
      </Field>
      <Button type="submit" size="lg" disabled={create.isPending || !draft.name.trim()}>
        {create.isPending ? <Spinner /> : null}
        创建并开始
      </Button>
    </form>
  );
}
