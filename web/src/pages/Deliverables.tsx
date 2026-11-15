// Deliverables board (资料库, full page): the library table, plus the
// “新做一个” chips that hand a templated request to the coordinator.
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { useProject } from "@/api/queries";
import { LibraryBrowser } from "@/components/app/Library";
import { Field, FormDialog } from "@/components/common";
import { Textarea } from "@/components/ui/textarea";
import { NavButton } from "@/components/shell/Shell";
import { deliverableKind } from "@/lib/present";
import { useProjectId } from "./Main";

const MAKE = ["design", "doc", "slides", "dashboard", "board"];

export { deliverableHref } from "@/lib/library";

export function DeliverablesPage() {
  const pid = useProjectId();
  const project = useProject(pid);
  const [making, setMaking] = useState<string | null>(null);
  return (
    <main className="flex min-h-full flex-col px-4 pb-6 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex w-full max-w-[1080px] flex-1 flex-col gap-5">
        <div className="flex items-start gap-1">
          <NavButton />
          <div className="flex flex-col gap-1">
            <Link to={`/projects/${pid}`} className="text-xs text-muted-foreground hover:text-foreground">
              {project.data?.name ?? "项目"}
            </Link>
            <h1 className="text-[22px] font-semibold">资料库</h1>
          </div>
        </div>
        <section className="flex flex-col gap-2">
          <span className="text-xs text-muted-foreground">让团队做一个新的（交给项目统筹，它会开讨论串去做）</span>
          <div className="flex flex-wrap gap-2">
            {MAKE.map((k) => {
              const look = deliverableKind(k);
              return (
                <button key={k} type="button" onClick={() => setMaking(k)} className="flex h-9 items-center gap-2 rounded-lg border bg-panel px-3 text-[13px] hover:border-input">
                  <span className="flex size-5 items-center justify-center rounded text-[11px] font-bold" style={{ background: look.bg, color: look.fg }}>
                    {look.glyph}
                  </span>
                  {look.label}
                </button>
              );
            })}
          </div>
        </section>
        <section className="flex min-h-[420px] flex-1 flex-col rounded-[14px] border bg-panel px-4 pt-3">
          <LibraryBrowser pid={pid} variant="page" />
        </section>
      </div>
      <MakeDialog pid={pid} kind={making} onClose={() => setMaking(null)} />
    </main>
  );
}

function MakeDialog({ pid, kind, onClose }: { pid: string; kind: string | null; onClose: () => void }) {
  const navigate = useNavigate();
  const [brief, setBrief] = useState("");
  const label = kind ? deliverableKind(kind).label : "";
  const request = useAction(() => projectActions.requestDeliverable(pid, kind!, brief.trim()), {
    success: "已交给项目统筹",
    onSuccess: () => {
      setBrief("");
      onClose();
      navigate(`/projects/${pid}`);
    },
  });
  return (
    <FormDialog
      open={kind !== null}
      onOpenChange={(o) => !o && onClose()}
      title={`做一个${label}`}
      description="会作为一条消息发给项目统筹，它决定怎么开讨论串去做。"
      submitLabel="交给统筹"
      busy={request.isPending}
      disabled={!brief.trim()}
      onSubmit={() => request.mutate()}
    >
      <Field label="要做什么">
        <Textarea rows={4} autoFocus value={brief} onChange={(e) => setBrief(e.target.value)} placeholder={`例如：给投资人的 v2 进展介绍`} />
      </Field>
    </FormDialog>
  );
}
