// A v1 conversation, read only (api.md §3.13): messages and its task list.
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { client, unwrap } from "@/api/client";
import { flattenMessages, useLegacyConversations, useMessages } from "@/api/queries";
import { Chat } from "@/components/app/Chat";
import { Loading } from "@/components/common";
import { NavButton } from "@/components/shell/Shell";

export function LegacyPage() {
  const { pid = "", cid = "" } = useParams();
  const list = useLegacyConversations(pid);
  const messages = useMessages(cid);
  const tasks = useQuery({ queryKey: ["c", cid, "legacy-tasks"], queryFn: () => unwrap(client.GET("/api/conversations/{cid}/tasks", { params: { path: { cid } } })) });
  const title = list.data?.find((c) => c.id === cid)?.title || "旧对话";
  return (
    <main className="px-4 pb-14 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[760px] flex-col gap-5">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1.5">
            <Link to={`/projects/${pid}/overview`} className="text-xs text-muted-foreground hover:text-foreground">
              项目首页 / 旧对话
            </Link>
            <h1 className="text-2xl font-semibold">{title}</h1>
            <p className="text-[13px] text-muted-foreground">v1 时期的对话，只读。新的需求请在项目里和统筹说。</p>
          </div>
        </div>
        {messages.isLoading ? <Loading /> : <Chat cid={cid} compact empty={<p className="text-[13px] text-muted-foreground">没有消息。</p>} />}
        {flattenMessages(messages.data).length > 0 && (tasks.data ?? []).length > 0 && (
          <section className="flex flex-col gap-2 rounded-[14px] border bg-panel p-5">
            <h2 className="text-[15px] font-semibold">任务</h2>
            {(tasks.data ?? []).map((t) => (
              <div key={t.id} className="flex gap-3 border-t border-[#F1EFEA] pt-2 text-[13px]">
                <span className="font-mono text-muted-foreground">{t.id}</span>
                <span className="font-mono">{t.assignee}</span>
                <span className="min-w-0 flex-1 truncate">{t.instruction}</span>
                <span className="text-muted-foreground">{t.status}</span>
              </div>
            ))}
          </section>
        )}
      </div>
    </main>
  );
}
