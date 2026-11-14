import { ArrowLeft } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ConfirmAction, DangerZone, Loading, Page, Panel, Spinner } from "@/components/common";
import { draftOf, draftPayload, ProjectFields, type ProjectDraft } from "@/components/ProjectFields";
import { Button } from "@/components/ui/button";
import { api, type Project } from "@/lib/api";
import { useAction } from "@/lib/mutations";
import { keys, useProject } from "@/lib/queries";

export function ProjectSettingsPage() {
  const { projectId = "" } = useParams();
  const project = useProject(projectId);
  const [draft, setDraft] = useState<ProjectDraft | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (project.data && draft === null) setDraft(draftOf(project.data));
  }, [project.data, draft]);

  const save = useAction(
    () => {
      const body = draftPayload(draft!);
      const json = body.repo ? body : { ...body, repo: undefined, branch: undefined, git_connection_id: undefined, clear_repo: true };
      return api<Project>(`/api/projects/${projectId}`, { method: "PATCH", json });
    },
    { invalidate: [keys.projects], success: "已保存。新的设置从下一条消息开始生效。", onSuccess: () => setDraft(null) },
  );
  const remove = useAction(() => api(`/api/projects/${projectId}`, { method: "DELETE" }), {
    invalidate: [keys.projects],
    success: "项目已删除",
    onSuccess: () => navigate("/projects"),
  });

  if (!project.data || !draft) return <Loading />;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  return (
    <Page
      width="narrow"
      title={`项目设置 · ${project.data.name}`}
      description="改动从下一条消息开始生效。换仓库后，员工会在新的目录里重新克隆。"
      before={
        <Button asChild variant="ghost" size="sm" className="self-start">
          <Link to={`/projects/${projectId}`}>
            <ArrowLeft /> 回到对话
          </Link>
        </Button>
      }
    >
      <form onSubmit={submit}>
        <Panel>
          <ProjectFields draft={draft} onChange={setDraft} />
          <div className="flex justify-end">
            <Button type="submit" disabled={save.isPending || !draft.name.trim()}>
              {save.isPending && <Spinner />} 保存
            </Button>
          </div>
        </Panel>
      </form>
      <DangerZone title="删除项目" description="项目下的对话和任务记录一起删除。节点上已克隆的代码不会被删。">
        <ConfirmAction title={`删除「${project.data.name}」？`} description="对话和任务记录会一起删除，无法恢复。" onConfirm={() => remove.mutate()} />
      </DangerZone>
    </Page>
  );
}
