// The ⋯ menu of a thread (list rows and the detail header): the 任务 / 交付物 / 成员 views,
// full screen, pause / resume, archive, unarchive and delete, plus the header's ✓ (archive)
// button. Archiving a thread that is still going and deleting always ask first.
import { Archive, ArchiveRestore, CircleCheck, ListChecks, Maximize2, MoreHorizontal, Package, Pause, Play, Trash2, Users } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import { ConfirmAction, IconButton } from "@/components/common";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { threadActive } from "@/lib/live";
import { cn } from "@/lib/utils";

type Confirm = "archive" | "delete" | null;

/** Views of a thread that open over the chat from the ⋯ menu. */
export type ThreadViewId = "tasks" | "deliverables" | "members";

export const THREAD_VIEWS: [ThreadViewId, string][] = [
  ["tasks", "任务"],
  ["deliverables", "交付物"],
  ["members", "成员"],
];

const VIEW_ICON = { tasks: ListChecks, deliverables: Package, members: Users } as const;

function archiveNotice(no: number) {
  return {
    title: `归档还在进行的 #${no}？`,
    description: "归档会结束这个讨论串：员工的会话关掉，节点上的工作目录清理（有未推送改动的会保留）。之后可以取消归档，它会回到“已暂停”，点继续接着做。",
  };
}

/** The header's ✓: archive the thread (asks first while it is still going); hidden once archived. */
export function ResolveButton({ thread }: { thread: ThreadView }) {
  const [confirming, setConfirming] = useState(false);
  const invalidate = [keys.threads(thread.project_id), keys.thread(thread.id), keys.waiting(thread.project_id)];
  const archive = useAction(() => threadActions.archive(thread.id), { invalidate, success: `#${thread.no} 已归档` });
  if (thread.status === "archived") return null;
  const notice = archiveNotice(thread.no);
  return (
    <>
      <IconButton label="归档讨论串" onClick={() => (threadActive(thread) ? setConfirming(true) : archive.mutate())} disabled={archive.isPending}>
        <CircleCheck strokeWidth={1.8} />
      </IconButton>
      <ConfirmAction open={confirming} onOpenChange={setConfirming} title={notice.title} description={notice.description} confirmLabel="归档" variant="default" onConfirm={() => archive.mutate()} />
    </>
  );
}

/** What the menu offers for a thread in this status. */
export function menuItems(t: Pick<ThreadView, "status">): { pause: boolean; resume: boolean; archive: boolean; unarchive: boolean } {
  return {
    pause: ["running", "waiting", "queued"].includes(t.status),
    resume: t.status === "paused",
    archive: t.status !== "archived",
    unarchive: t.status === "archived",
  };
}

interface MenuProps {
  thread: ThreadView;
  fullHref?: string;
  onDeleted?: () => void;
  /** Adds 任务 / 交付物 / 成员 at the top (the detail header). */
  onView?: (view: ThreadViewId) => void;
  className?: string;
}

export function ThreadMenu({ thread, fullHref, onDeleted, onView, className }: MenuProps) {
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState<Confirm>(null);
  const invalidate = [keys.threads(thread.project_id), keys.thread(thread.id), keys.waiting(thread.project_id)];
  const update = useAction((status: "paused" | "running") => threadActions.update(thread.id, { status }), { invalidate });
  const archive = useAction(() => threadActions.archive(thread.id), { invalidate, success: `#${thread.no} 已归档` });
  const unarchive = useAction(() => threadActions.unarchive(thread.id), { invalidate, success: `#${thread.no} 已取消归档` });
  const remove = useAction(() => threadActions.remove(thread.id), { invalidate, success: `#${thread.no} 已删除`, onSuccess: () => onDeleted?.() });
  const items = menuItems(thread);
  const active = threadActive(thread);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <IconButton label={`讨论串 #${thread.no} 的操作`} className={cn("size-8", className)}>
            <MoreHorizontal strokeWidth={1.8} />
          </IconButton>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-44" onCloseAutoFocus={(e) => e.preventDefault()}>
          {onView &&
            THREAD_VIEWS.map(([id, label]) => {
              const Icon = VIEW_ICON[id];
              return (
                <DropdownMenuItem key={id} onSelect={() => onView(id)}>
                  <Icon /> {label}
                </DropdownMenuItem>
              );
            })}
          {onView && <DropdownMenuSeparator />}
          {fullHref && (
            <DropdownMenuItem onSelect={() => navigate(fullHref)}>
              <Maximize2 /> 全屏打开
            </DropdownMenuItem>
          )}
          {items.pause && (
            <DropdownMenuItem onSelect={() => update.mutate("paused")}>
              <Pause /> 暂停
            </DropdownMenuItem>
          )}
          {items.resume && (
            <DropdownMenuItem onSelect={() => update.mutate("running")}>
              <Play /> 继续
            </DropdownMenuItem>
          )}
          {items.archive && (
            <DropdownMenuItem onSelect={() => (active ? setConfirm("archive") : archive.mutate())}>
              <Archive /> 归档
            </DropdownMenuItem>
          )}
          {items.unarchive && (
            <DropdownMenuItem onSelect={() => unarchive.mutate()}>
              <ArchiveRestore /> 取消归档
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setConfirm("delete")} className="text-destructive focus:text-destructive">
            <Trash2 /> 删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmAction
        open={confirm === "archive"}
        onOpenChange={(o) => setConfirm(o ? "archive" : null)}
        {...archiveNotice(thread.no)}
        confirmLabel="归档"
        variant="default"
        onConfirm={() => archive.mutate()}
      />
      <ConfirmAction
        open={confirm === "delete"}
        onOpenChange={(o) => setConfirm(o ? "delete" : null)}
        title={`删除 #${thread.no} ${thread.title}？`}
        description="会关掉员工的会话、取消没做完的任务、删掉节点上的工作目录（包括未推送的改动）和成员分支，对话、任务和审批记录一并删除，不能恢复。它做的交付物会留在资料库里。"
        confirmLabel="删除"
        onConfirm={() => remove.mutate()}
      />
    </>
  );
}
