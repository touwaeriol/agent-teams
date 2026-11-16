// A thread on its own page: the same chat-only view as the right panel (header, conversation
// with you and the lead, message box), at reading width. Phones and narrow screens get it
// for /projects/:pid/threads/:no; wide screens open the panel there and this page at /full.
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useProject, useThreadByNo } from "@/api/queries";
import { detailViewOf, ThreadDetail, type ThreadViewId } from "@/components/app/ThreadDetail";
import { EmptyState, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { useMedia } from "@/lib/media";
import { MainPage, threadPath } from "./Main";

/**
 * /projects/:pid/threads/:no(/:tab): on wide screens the main board with this thread open in
 * the right panel (the tab names a view open over the chat); narrower screens get the thread page.
 */
export function ThreadRoute() {
  const { no = "", tab } = useParams();
  const wide = useMedia("(min-width: 1024px)");
  if (wide) return <MainPage openNo={Number(no) || null} view={detailViewOf(tab)} />;
  return <ThreadPage />;
}

export function ThreadPage() {
  const { pid = "", no = "", tab } = useParams();
  const project = useProject(pid);
  const { thread, isLoading } = useThreadByNo(pid, Number(no));
  const wide = useMedia("(min-width: 1024px)");
  const navigate = useNavigate();
  const [view, setView] = useState<ThreadViewId | null>(detailViewOf(tab));
  if (isLoading || !project.data) return <Loading className="h-full" />;
  if (!thread) return <EmptyState title={`找不到讨论串 #${no}`} description="它可能已被归档或删除。" action={<Button asChild><Link to={`/projects/${pid}`}>回到项目</Link></Button>} />;
  const home = () => navigate(`/projects/${pid}`);
  return (
    <div className="flex h-full min-h-0 flex-col bg-panel">
      <ThreadDetail
        key={thread.id}
        pid={pid}
        thread={thread}
        view={view}
        onView={setView}
        onBack={home}
        onClose={home}
        expand={wide ? { to: threadPath(pid, thread.no), label: "退出全屏", collapse: true } : undefined}
        layout="page"
      />
    </div>
  );
}
