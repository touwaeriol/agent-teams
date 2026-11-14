import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import { draftPayload, ProjectFields, type ProjectDraft } from "@/components/ProjectFields";
import { errorText, PageHeader, Spinner } from "@/components/shared";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { api, type Project } from "@/lib/api";
import { keys, useProject } from "@/lib/queries";

export function ProjectSettingsPage() {
  const { projectId = "" } = useParams();
  const project = useProject(projectId);
  const [draft, setDraft] = useState<ProjectDraft | null>(null);
  const client = useQueryClient();
  const navigate = useNavigate();

  useEffect(() => {
    if (project.data && draft === null) {
      const p = project.data;
      setDraft({
        name: p.name,
        description: p.description,
        context: p.context,
        repo: p.repo ?? "",
        branch: p.branch,
        git_connection_id: p.git_connection_id ?? "",
      });
    }
  }, [project.data, draft]);

  const save = useMutation({
    mutationFn: () => {
      const body = draftPayload(draft!);
      const json = body.repo ? body : { ...body, repo: undefined, branch: undefined, git_connection_id: undefined, clear_repo: true };
      return api<Project>(`/api/projects/${projectId}`, { method: "PATCH", json });
    },
    onSuccess: (p) => {
      client.setQueryData(keys.project(projectId), p);
      client.invalidateQueries({ queryKey: keys.projects });
      setDraft(null);
      toast.success("已保存。新的设置从下一条消息开始生效。");
    },
    onError: (e) => toast.error(errorText(e)),
  });

  const remove = useMutation({
    mutationFn: () => api(`/api/projects/${projectId}`, { method: "DELETE" }),
    onSuccess: () => {
      client.invalidateQueries({ queryKey: keys.projects });
      toast.success("项目已删除");
      navigate("/projects");
    },
    onError: (e) => toast.error(errorText(e)),
  });

  if (!project.data || !draft) return <div className="p-10"><Spinner /></div>;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  return (
    <div className="container flex max-w-3xl flex-col gap-5 py-6">
      <Button asChild variant="ghost" size="sm" className="self-start">
        <Link to={`/projects/${projectId}`}>
          <ArrowLeft /> 回到对话
        </Link>
      </Button>
      <PageHeader title={`项目设置 · ${project.data.name}`} description="改动从下一条消息开始生效。换仓库后，员工会在新的目录里重新克隆。" />
      <form onSubmit={submit} className="flex flex-col gap-5 rounded-xl border bg-panel p-5">
        <ProjectFields draft={draft} onChange={setDraft} />
        <div className="flex justify-end">
          <Button type="submit" disabled={save.isPending || !draft.name.trim()}>
            {save.isPending && <Spinner />} 保存
          </Button>
        </div>
      </form>
      <section className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-panel p-5">
        <div>
          <h2 className="font-semibold">删除项目</h2>
          <p className="text-[13px] text-muted-foreground">项目下的对话和任务记录一起删除。节点上已克隆的代码不会被删。</p>
        </div>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive">
              <Trash2 /> 删除
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>删除「{project.data.name}」？</AlertDialogTitle>
              <AlertDialogDescription>对话和任务记录会一起删除，无法恢复。</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction onClick={() => remove.mutate()}>删除</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </section>
    </div>
  );
}
