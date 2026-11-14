import { ExternalLink, FolderGit2, GitBranch, MessageSquare, Plus, Settings } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { EmptyState, errorText, Field, Loading, PageHeader, Panel, Spinner, SplitLayout, StatusBadge } from "@/components/common";
import { draftPayload, emptyDraft, ProjectFields, type ProjectDraft } from "@/components/ProjectFields";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api, type Conversation, type Project } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { keys, useProjects } from "@/lib/queries";
import { timeAgo } from "@/lib/utils";

export function ProjectsPage() {
  const projects = useProjects();
  const [params] = useSearchParams();
  return (
    <div className="container py-6">
      <SplitLayout
        main={
          <>
            <PageHeader
              title="项目"
              description="一个项目 = 一个 GitHub 仓库 + 项目上下文 + 和负责人的对话。你只和负责人说话，它负责拆任务、派给员工，员工在节点上各自的仓库克隆里干活。"
            />
            {projects.isLoading && <Loading />}
            {projects.data?.length === 0 && (
              <EmptyState icon={<FolderGit2 />} title="还没有项目" description="在右边填好名称、上下文，选一个仓库，就能开始和负责人对话。" />
            )}
            {projects.data?.map((p) => <ProjectCard key={p.id} project={p} />)}
          </>
        }
        side={<NewProject highlight={params.get("new") === "1"} />}
      />
    </div>
  );
}

function ProjectCard({ project }: { project: Project }) {
  return (
    <Panel
      as="article"
      className="transition-colors hover:border-input"
      title={
        <Link to={`/projects/${project.id}`} className="text-[17px] hover:text-accent-foreground">
          {project.name}
        </Link>
      }
      description={project.description || undefined}
      action={<span className="text-xs text-muted-foreground">更新于 {timeAgo(project.updated_at)}</span>}
    >
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
          <StatusBadge tone="warn" label="未关联仓库" />
        )}
        {project.context && <StatusBadge tone="info" label="有项目上下文" />}
      </div>
      <div className="flex flex-wrap gap-2">
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
    </Panel>
  );
}

function NewProject({ highlight }: { highlight: boolean }) {
  const [draft, setDraft] = useState<ProjectDraft>(emptyDraft);
  const [firstMessage, setFirstMessage] = useState("");
  const navigate = useNavigate();

  const create = useAction(
    async () => {
      const project = await api<Project>("/api/projects", { method: "POST", json: draftPayload(draft) });
      const first = firstMessage.trim();
      const conversation = await api<Conversation>("/api/conversations", {
        method: "POST",
        json: { title: first.slice(0, 40) || "第一次对话", project_id: project.id },
      });
      if (first) {
        await api(`/api/conversations/${conversation.id}/messages`, { method: "POST", json: { content: first } }).catch((e) =>
          toast.error(`项目已创建，但第一条消息没发出去：${errorText(e)}`),
        );
      }
      return { project, conversation };
    },
    {
      invalidate: [keys.projects],
      success: ({ project }) => `已创建项目「${project.name}」`,
      onSuccess: ({ project, conversation }) => navigate(`/projects/${project.id}/c/${conversation.id}`),
    },
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    create.mutate();
  };

  return (
    <form onSubmit={submit}>
      <Panel tone={highlight ? "highlight" : "default"} title={<span className="text-[17px]">新建项目</span>} icon={<Plus className="!text-primary" />}>
        <ProjectFields draft={draft} onChange={setDraft} />
        <Field label="要做什么（可选，会成为对话里的第一条消息）">
          <Textarea rows={4} placeholder="目标、范围、期限、验收方式" value={firstMessage} onChange={(e) => setFirstMessage(e.target.value)} />
        </Field>
        <Button type="submit" size="lg" disabled={create.isPending || !draft.name.trim()}>
          {create.isPending && <Spinner />}
          创建并开始
        </Button>
      </Panel>
    </form>
  );
}
