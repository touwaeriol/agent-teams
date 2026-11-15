// Cards that wait for the user: permission requests, questions and PR
// acceptance. Three looks: `soft` (thread panel), `plain` (Thread page aside),
// `mobile` (44 px buttons in a grid).
import { useState, type ReactNode } from "react";
import { conversations, threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { QuestionView, ThreadView } from "@/api/views";
import { Spinner } from "@/components/common";
import { approvalCommand, threadBases, threadPRs } from "@/lib/present";
import { cn } from "@/lib/utils";
import { safeHref } from "@/lib/safeurl";

export type CardVariant = "soft" | "plain" | "mobile";

const FRAME: Record<CardVariant, string> = {
  soft: "border-[#F0C9A8] bg-[#FFF9F4] px-3.5 py-3",
  plain: "border-[#F0C9A8] bg-panel px-4 py-3.5",
  mobile: "border-[#F0C9A8] bg-[#FFF9F4] p-3",
};
const CODE: Record<CardVariant, string> = {
  soft: "bg-white border border-[#F3E2D3]",
  plain: "bg-[#FFF9F4] border border-[#F3E2D3]",
  mobile: "bg-white",
};

function Frame({ variant, head, code, children, label }: { variant: CardVariant; head: ReactNode; code?: ReactNode; children: ReactNode; label: string }) {
  return (
    <section aria-label={label} className={cn("flex flex-col gap-2.5 rounded-xl border", FRAME[variant])}>
      <div className="flex items-start justify-between gap-2 text-[13px]">{head}</div>
      {code && <code className={cn("block whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-lg px-2.5 py-2 font-mono text-xs", CODE[variant])}>{code}</code>}
      {children}
    </section>
  );
}

function Actions({ variant, children, count }: { variant: CardVariant; children: ReactNode; count: number }) {
  if (variant === "mobile") return <span className="grid gap-1.5" style={{ gridTemplateColumns: `repeat(${count}, minmax(0, 1fr))` }}>{children}</span>;
  return <span className="flex flex-wrap gap-2">{children}</span>;
}

type Kind = "primary" | "outline" | "danger";
function CardButton({ kind, variant, onClick, busy, children }: { kind: Kind; variant: CardVariant; onClick: () => void; busy?: boolean; children: ReactNode }) {
  const look = { primary: "border-transparent bg-primary text-primary-foreground", outline: "border-input bg-white", danger: "border-[#EBC3C3] bg-white text-destructive" }[kind];
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      className={cn("inline-flex items-center justify-center gap-1.5 rounded-lg border px-3 text-[13px] disabled:opacity-60", variant === "mobile" ? "h-11 rounded-[10px] text-sm" : "h-8", look)}
    >
      {busy && <Spinner className="size-3.5" />}
      {children}
    </button>
  );
}

function ReasonBox({ placeholder, confirm, onSubmit, onCancel, busy }: { placeholder: string; confirm: string; onSubmit: (text: string) => void; onCancel: () => void; busy?: boolean }) {
  const [text, setText] = useState("");
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(text.trim());
      }}
    >
      <textarea
        autoFocus
        rows={2}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={placeholder}
        className="w-full resize-none rounded-lg border border-input bg-white px-2.5 py-2 text-[13px] outline-none focus:ring-2 focus:ring-ring/40"
      />
      <span className="flex gap-2">
        <button type="submit" disabled={busy} className="h-8 rounded-lg bg-primary px-3 text-[13px] text-primary-foreground disabled:opacity-60">
          {confirm}
        </button>
        <button type="button" onClick={onCancel} className="h-8 rounded-lg border border-input bg-white px-3 text-[13px]">
          取消
        </button>
      </span>
    </form>
  );
}

function useAnswer(cid: string, q: QuestionView, pid: string) {
  return useAction((v: { answer: string; reason?: string }) => conversations.answer(cid, q.id, v.answer, v.reason), {
    invalidate: [keys.questions(cid), keys.waiting(pid), keys.threads(pid)],
  });
}

export function askerTitle(q: QuestionView, leadName?: string): string {
  if (q.kind === "approval") return `${q.asker} 请求权限`;
  return q.asker === leadName || q.asker.startsWith("lead") ? "组长提问" : `${q.asker} 提问`;
}

/** A permission request: 放行 / 总是允许 / 拒绝 (with an optional reason sent to the employee). */
export function ApprovalCard({ cid, pid, question, variant = "soft" }: { cid: string; pid: string; question: QuestionView; variant?: CardVariant }) {
  const answer = useAnswer(cid, question, pid);
  const [denying, setDenying] = useState(false);
  const head = (
    <>
      <span>
        <span className="font-mono">{question.asker}</span> 请求权限{question.detail.tool_name ? <span className="text-muted-foreground"> · {question.detail.tool_name}</span> : null}
      </span>
      {variant !== "mobile" && <span className="shrink-0 text-xs text-[#B4530B]">等你处理</span>}
    </>
  );
  return (
    <Frame variant={variant} head={head} code={approvalCommand(question)} label={`${question.asker} 请求权限`}>
      {question.detail.description && <p className="text-xs leading-5 text-muted-foreground">{question.detail.description}</p>}
      {denying ? (
        <ReasonBox
          placeholder="拒绝理由（可选，会发给员工）"
          confirm="确认拒绝"
          busy={answer.isPending}
          onCancel={() => setDenying(false)}
          onSubmit={(reason) => answer.mutate({ answer: "deny", reason: reason || undefined })}
        />
      ) : (
        <Actions variant={variant} count={3}>
          <CardButton kind="primary" variant={variant} busy={answer.isPending && answer.variables?.answer === "allow"} onClick={() => answer.mutate({ answer: "allow" })}>
            放行
          </CardButton>
          <CardButton kind="outline" variant={variant} busy={answer.isPending && answer.variables?.answer === "allow_always"} onClick={() => answer.mutate({ answer: "allow_always" })}>
            总是允许
          </CardButton>
          <CardButton kind="danger" variant={variant} onClick={() => setDenying(true)}>
            拒绝
          </CardButton>
        </Actions>
      )}
    </Frame>
  );
}

/** A question for the user: one button per option, or a typed answer. */
export function QuestionCard({ cid, pid, question, variant = "soft", leadName }: { cid: string; pid: string; question: QuestionView; variant?: CardVariant; leadName?: string }) {
  const answer = useAnswer(cid, question, pid);
  const [typing, setTyping] = useState(question.options.length === 0);
  const head = (
    <>
      <span>
        {askerTitle(question, leadName)} · <span className="text-[#B4530B]">等你处理</span>
      </span>
    </>
  );
  return (
    <Frame variant={variant} head={head} code={question.text} label={askerTitle(question, leadName)}>
      {typing ? (
        <ReasonBox
          placeholder="写下你的回答"
          confirm="回答"
          busy={answer.isPending}
          onCancel={() => setTyping(question.options.length === 0 ? true : false)}
          onSubmit={(text) => text && answer.mutate({ answer: text })}
        />
      ) : (
        <Actions variant={variant} count={Math.min(3, question.options.length + 1)}>
          {question.options.map((o, i) => (
            <CardButton key={o} kind={i === 0 ? "primary" : "outline"} variant={variant} busy={answer.isPending && answer.variables?.answer === o} onClick={() => answer.mutate({ answer: o })}>
              {o}
            </CardButton>
          ))}
          <CardButton kind="outline" variant={variant} onClick={() => setTyping(true)}>
            其他回答
          </CardButton>
        </Actions>
      )}
    </Frame>
  );
}

/** Either card, by kind. */
export function WaitingCard(props: { cid: string; pid: string; question: QuestionView; variant?: CardVariant; leadName?: string }) {
  return props.question.kind === "approval" ? <ApprovalCard {...props} /> : <QuestionCard {...props} />;
}

/** PR acceptance: 验收通过并合并 / 提修改意见 (reason goes to the lead). */
export function ReviewCard({ thread, variant = "soft", files }: { thread: ThreadView; variant?: CardVariant; files?: number }) {
  const [rejecting, setRejecting] = useState(false);
  const invalidate = [keys.threads(thread.project_id), keys.thread(thread.id), keys.waiting(thread.project_id)];
  const accept = useAction(() => threadActions.accept(thread.id), { invalidate, success: "已验收，平台会在检查通过后合并" });
  const reject = useAction((reason: string) => threadActions.reject(thread.id, reason), { invalidate, success: "修改意见已发给组长" });
  const prs = threadPRs(thread);
  const head = (
    <span>
      {prs.length > 1 ? `${prs.length} 个 PR` : thread.pr_number ? `PR #${thread.pr_number}` : "讨论串"} 等你验收 · <span className="text-[#B4530B]">等你处理</span>
    </span>
  );
  const code = `${thread.branch} → ${threadBases(thread) || "基准分支"}${files ? ` · 改了 ${files} 个文件` : ""}`;
  return (
    <Frame variant={variant} head={head} code={code} label="等你验收">
      {thread.summary && <p className="text-xs leading-5 text-[#4A4843]">{thread.summary}</p>}
      {rejecting ? (
        <ReasonBox placeholder="哪里要改？会发给组长" confirm="发给组长" busy={reject.isPending} onCancel={() => setRejecting(false)} onSubmit={(r) => r && reject.mutate(r)} />
      ) : (
        <Actions variant={variant} count={2}>
          <CardButton kind="primary" variant={variant} busy={accept.isPending} onClick={() => accept.mutate()}>
            验收通过并合并
          </CardButton>
          <CardButton kind="outline" variant={variant} onClick={() => setRejecting(true)}>
            提修改意见
          </CardButton>
          {variant !== "mobile" &&
            prs.map((pr) => (
              <a key={pr.url ?? pr.number} href={safeHref(pr.url)} target="_blank" rel="noreferrer" className="inline-flex h-8 items-center px-1 text-[13px] text-muted-foreground hover:text-foreground">
                {prs.length > 1 ? `${pr.repo} #${pr.number} ↗` : "在 GitHub 上看 ↗"}
              </a>
            ))}
        </Actions>
      )}
    </Frame>
  );
}
