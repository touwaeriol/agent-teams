import { useQueryClient } from "@tanstack/react-query";
import { CircleHelp, ShieldAlert } from "lucide-react";
import { useState, type FormEvent } from "react";
import { CopyBlock, Panel, Spinner, StatusBadge } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, ApiError, type Question } from "@/lib/api";
import { answerLabel, APPROVAL_ANSWERS, approvalBody, upsertById } from "@/lib/comms";
import { useAction } from "@/lib/mutations";
import { keys } from "@/lib/queries";
import { clock } from "@/lib/utils";

/**
 * A worker's question or permission request shown in the chat. Open cards
 * take an answer; handled ones shrink to a one-line record.
 */
export function QuestionCard({ conversationId, question }: { conversationId: string; question: Question }) {
  const answer = useAnswer(conversationId, question.id);
  const open = question.status === "open";
  const approval = question.kind === "approval";
  return (
    <Panel
      as="article"
      padding="sm"
      tone={open ? "highlight" : "default"}
      label={approval ? `${question.asker} 的权限请求` : `${question.asker} 的提问`}
      className={open ? "shadow-[0_2px_12px_-4px_rgb(0_0_0/0.08)]" : "bg-transparent"}
      title={<CardTitle question={question} />}
      action={open ? <StatusBadge tone="warn" label="等你处理" /> : <StatusBadge tone={question.status === "cancelled" ? "neutral" : "ok"} label={answerLabel(question)} />}
    >
      {approval ? <ApprovalBody question={question} /> : <Markdown className="text-[14px] leading-6">{question.text}</Markdown>}
      {open && (approval ? <ApprovalButtons busy={answer.isPending} onAnswer={answer.mutate} /> : <QuestionAnswer question={question} busy={answer.isPending} onAnswer={answer.mutate} />)}
    </Panel>
  );
}

function CardTitle({ question }: { question: Question }) {
  const Icon = question.kind === "approval" ? ShieldAlert : CircleHelp;
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm font-normal">
      <Icon />
      <span className="font-mono font-medium">{question.asker}</span>
      <span className="text-muted-foreground">{question.kind === "approval" ? "请求权限" : "问你"}</span>
      {question.task_id && <span className="font-mono text-xs text-muted-foreground">· {question.task_id}</span>}
      <span className="text-xs text-muted-foreground">· {clock(question.created_at)}</span>
    </span>
  );
}

function useAnswer(conversationId: string, questionId: string) {
  const client = useQueryClient();
  const key = keys.questions(conversationId);
  const post = async (answer: string) => {
    try {
      return await api<Question>(`/api/conversations/${conversationId}/questions/${questionId}/answer`, { method: "POST", json: { answer } });
    } catch (e) {
      // 409: someone (or another tab) already answered; show the real state.
      if (e instanceof ApiError && e.status === 409) void client.invalidateQueries({ queryKey: key });
      throw e;
    }
  };
  return useAction(post, { onSuccess: (q) => client.setQueryData<Question[]>(key, (old = []) => upsertById(old, q)) });
}

function ApprovalBody({ question }: { question: Question }) {
  const { tool, command, input } = approvalBody(question);
  return (
    <div className="flex flex-col gap-2 text-sm">
      <span>
        想使用工具 <span className="rounded bg-raised px-1.5 py-0.5 font-mono text-[13px]">{tool}</span>
      </span>
      {question.text && question.text !== tool && <p className="text-muted-foreground">{question.text}</p>}
      <CopyBlock text={command ?? input} className="max-h-60 overflow-y-auto" />
    </div>
  );
}

function ApprovalButtons({ busy, onAnswer }: { busy: boolean; onAnswer: (answer: string) => void }) {
  return (
    <div className="flex flex-wrap gap-2">
      <Button size="sm" disabled={busy} onClick={() => onAnswer(APPROVAL_ANSWERS.allow)}>
        {busy && <Spinner />} 放行
      </Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={() => onAnswer(APPROVAL_ANSWERS.always)} title="本对话里这个员工以后再用这个工具不再询问">
        总是允许
      </Button>
      <Button size="sm" variant="destructive" disabled={busy} onClick={() => onAnswer(APPROVAL_ANSWERS.deny)}>
        拒绝
      </Button>
    </div>
  );
}

function QuestionAnswer({ question, busy, onAnswer }: { question: Question; busy: boolean; onAnswer: (answer: string) => void }) {
  const [text, setText] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (text.trim()) onAnswer(text.trim());
  };
  return (
    <div className="flex flex-col gap-2">
      {question.options.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {question.options.map((o) => (
            <Button key={o} size="sm" variant="outline" disabled={busy} onClick={() => onAnswer(o)}>
              {o}
            </Button>
          ))}
        </div>
      )}
      <form onSubmit={submit} className="flex gap-2">
        <Input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={question.options.length ? "或者写你自己的回答" : "写下你的回答"}
          aria-label={`回答 ${question.asker} 的问题`}
          className="h-8 text-sm"
        />
        <Button type="submit" size="sm" disabled={busy || !text.trim()}>
          {busy && <Spinner />} 回答
        </Button>
      </form>
    </div>
  );
}
