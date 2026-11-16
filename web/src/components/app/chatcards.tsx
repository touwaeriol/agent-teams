// Cards drawn inside the chats (architecture.md §6.6), the way Claude's own chat shows them:
// AskCard (a question for the user: letter badges, 推荐, descriptions, 其他, collapsed to the
// chosen options once answered), TodoCard (the live checklist), PlanCard (Markdown plan),
// ArtifactCard (a published deliverable) and the quiet "会话已恢复" line.
import { Check, ChevronDown, ChevronRight, Circle, CircleCheck, ExternalLink, ListTodo, NotebookPen } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { AskChoice, AskItem, DeliverableView, MessageView, QuestionView, TodoItem } from "@/api/views";
import { Spinner } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { answerBody, answeredRows, askItems, complete, emptyChoices, letter, oneClick, toggle, typeOther } from "@/lib/ask";
import { fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { safeHref } from "@/lib/safeurl";
import { cn } from "@/lib/utils";
import { Spin } from "./bits";
import { FILE_ICONS } from "./Library";

const CARD = "rounded-2xl border border-[#E7E4DD] bg-panel";

/** A question for the user. Open: pick (A/B/C…, 推荐, descriptions) or write 其他, then submit. Answered: the question with the chosen rows ticked. */
export function AskCard({ cid, pid, question, who }: { cid: string; pid: string; question: QuestionView; who?: string }) {
  const items = askItems(question);
  if (question.status !== "open") return <AnsweredAsk question={question} items={items} who={who} />;
  return <OpenAsk cid={cid} pid={pid} question={question} items={items} who={who} />;
}

function OpenAsk({ cid, pid, question, items, who }: { cid: string; pid: string; question: QuestionView; items: AskItem[]; who?: string }) {
  const [choices, setChoices] = useState<AskChoice[]>(() => emptyChoices(items));
  const answer = useAction((c: AskChoice[]) => {
    const body = answerBody(question, c);
    return conversations.answer(cid, question.id, body.answer, undefined, body.answers);
  }, { invalidate: [keys.questions(cid), keys.waiting(pid), keys.threads(pid)] });
  const quick = oneClick(items);
  const set = (i: number, c: AskChoice) => setChoices((all) => all.map((x, j) => (j === i ? c : x)));
  const pick = (i: number, label: string) => {
    const next = toggle(choices[i], label, Boolean(items[i].multi_select));
    if (quick && next.selected.length) return answer.mutate([next]);
    set(i, next);
  };
  return (
    <section aria-label={`${who ?? question.asker} 的提问`} className={cn(CARD, "flex flex-col gap-4 px-4 py-3.5")}>
      {items.map((item, i) => (
        <AskBlock key={i} item={item} choice={choices[i]} busy={answer.isPending} who={i === 0 ? who : undefined} onPick={(l) => pick(i, l)} onOther={(t) => set(i, typeOther(choices[i], t, Boolean(item.multi_select)))} />
      ))}
      {(!quick || choices[0].other || answer.isPending) && (
        <div className="flex items-center justify-end gap-2">
          {answer.isPending && <Spinner className="size-3.5" />}
          {(!quick || choices[0].other) && (
            <button type="button" disabled={!complete(choices) || answer.isPending} onClick={() => answer.mutate(choices)} className="h-8 rounded-lg bg-primary px-3.5 text-[13px] text-primary-foreground disabled:opacity-40">
              提交
            </button>
          )}
        </div>
      )}
    </section>
  );
}

function AskBlock({ item, choice, busy, who, onPick, onOther }: { item: AskItem; choice: AskChoice; busy: boolean; who?: string; onPick: (label: string) => void; onOther: (text: string) => void }) {
  const multi = Boolean(item.multi_select);
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5" disabled={busy}>
      <legend className="mb-2 flex w-full items-start justify-between gap-3 text-[15px] leading-6 text-[#3D3B36]">
        <span>
          {item.header && <span className="mr-2 rounded-md bg-[#F1EFEA] px-1.5 py-0.5 align-[2px] text-[11px] text-muted-foreground">{item.header}</span>}
          {item.question}
        </span>
        {who && <span className="shrink-0 pt-0.5 text-xs text-[#B4530B]">{who} · 等你回答</span>}
      </legend>
      {multi && <span className="-mt-1 mb-1 text-xs text-muted-foreground">可多选</span>}
      {item.options.map((o, i) => {
        const on = choice.selected.includes(o.label);
        return (
          <button
            key={o.label}
            type="button"
            role={multi ? "checkbox" : "radio"}
            aria-checked={on}
            onClick={() => onPick(o.label)}
            className={cn("flex w-full items-start gap-3 rounded-xl px-3 py-2.5 text-left transition-colors", on ? "bg-[#F1EFEA]" : "hover:bg-[#F7F6F3]")}
          >
            <Badge>{letter(i)}</Badge>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                {o.label}
                {o.recommended && <span className="rounded-md bg-[#ECEAE4] px-1.5 py-px text-[11px] font-normal text-[#5E5B54]">推荐</span>}
              </span>
              {o.description && <span className="text-[13px] leading-5 text-muted-foreground">{o.description}</span>}
            </span>
            {on && <Check aria-hidden className="mt-1.5 size-4 shrink-0 text-[#5E5B54]" strokeWidth={2} />}
          </button>
        );
      })}
      <label className={cn("flex items-center gap-3 rounded-xl px-3 py-2", choice.other ? "bg-[#F1EFEA]" : "")}>
        <Badge>{letter(item.options.length)}</Badge>
        <input
          value={choice.other ?? ""}
          onChange={(e) => onOther(e.target.value)}
          placeholder="其他（自己写）"
          aria-label={`其他回答：${item.question}`}
          className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground/80"
        />
      </label>
    </fieldset>
  );
}

function Badge({ children }: { children: string }) {
  return <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-[#ECEAE4] text-[13px] font-medium text-[#5E5B54]">{children}</span>;
}

function AnsweredAsk({ question, items, who }: { question: QuestionView; items: AskItem[]; who?: string }) {
  const rows = answeredRows(question);
  const cancelled = question.status === "cancelled";
  return (
    <section aria-label={`${who ?? question.asker} 的提问（已回答）`} className={cn(CARD, "flex flex-col gap-3 px-4 py-3.5")}>
      {items.map((item, i) => {
        const row = rows[i] ?? { question: item.question, selected: [] };
        return (
          <div key={i} className="flex flex-col gap-1.5">
            <p className="text-[15px] leading-6 text-[#3D3B36]">
              {item.header && items.length > 1 && <span className="mr-2 rounded-md bg-[#F1EFEA] px-1.5 py-0.5 align-[2px] text-[11px] text-muted-foreground">{item.header}</span>}
              {item.question}
            </p>
            {row.selected.map((label) => {
              const idx = item.options.findIndex((o) => o.label === label);
              const o = item.options[idx];
              return <ChosenRow key={label} badge={letter(Math.max(idx, 0))} label={label} recommended={o?.recommended} description={o?.description} />;
            })}
            {row.other && <ChosenRow badge={letter(item.options.length)} label={row.other} />}
            {cancelled && <p className="text-xs text-muted-foreground">问题已取消</p>}
          </div>
        );
      })}
    </section>
  );
}

function ChosenRow({ badge, label, recommended, description }: { badge: string; label: string; recommended?: boolean; description?: string }) {
  return (
    <div className="flex items-start gap-3 rounded-xl bg-[#F1EFEA] px-3 py-2.5">
      <Badge>{badge}</Badge>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
          {label}
          {recommended && <span className="rounded-md bg-[#E4E1DA] px-1.5 py-px text-[11px] font-normal text-[#5E5B54]">推荐</span>}
        </span>
        {description && <span className="text-[13px] leading-5 text-muted-foreground">{description}</span>}
      </span>
      <Check aria-label="已选" className="mt-1.5 size-4 shrink-0 text-[#5E5B54]" strokeWidth={2} />
    </div>
  );
}

/** The employee's TodoWrite checklist, updated in place; a spinner marks the item in progress. */
export function TodoCard({ message }: { message: MessageView }) {
  const todos: TodoItem[] = message.meta.todos ?? [];
  const done = todos.filter((t) => t.status === "completed").length;
  const finished = done === todos.length;
  const [open, setOpen] = useState(true);
  return (
    <section aria-label="待办清单" className={cn(CARD, "flex flex-col px-4 py-3")}>
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex items-center gap-2 text-left text-[13px] text-muted-foreground">
        <ListTodo aria-hidden className="size-4" strokeWidth={1.8} />
        <span className="flex-1">
          {message.meta.employee ?? message.author} 的待办 · {done}/{todos.length}
          {finished ? " 已完成" : ""}
        </span>
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
      </button>
      {open && (
        <ul className="mt-2.5 flex flex-col gap-1.5">
          {todos.map((t, i) => (
            <li key={i} className="flex items-start gap-2.5 text-sm leading-6">
              <TodoMark status={t.status} />
              <span className={cn(t.status === "completed" && "text-muted-foreground line-through decoration-[#C9C5BC]", t.status === "in_progress" && "font-medium")}>
                {t.status === "in_progress" && t.active_form ? t.active_form : t.content}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function TodoMark({ status }: { status: TodoItem["status"] }) {
  if (status === "completed") return <CircleCheck aria-label="已完成" className="mt-1 size-4 shrink-0 text-[#2E7D4F]" strokeWidth={1.8} />;
  if (status === "in_progress") return <Spin color="#2F6FDB" size={14} className="mt-[5px] shrink-0" label="进行中" />;
  return <Circle aria-label="未开始" className="mt-1 size-4 shrink-0 text-[#B7B3AA]" strokeWidth={1.8} />;
}

/** An ExitPlanMode plan, as Markdown; long plans fold. */
export function PlanCard({ message }: { message: MessageView }) {
  const long = message.content.length > 900;
  const [open, setOpen] = useState(!long);
  return (
    <section aria-label="计划" className={cn(CARD, "flex flex-col gap-2 px-4 py-3")}>
      <span className="flex items-center gap-2 text-[13px] text-muted-foreground">
        <NotebookPen aria-hidden className="size-4" strokeWidth={1.8} />
        {message.meta.employee ?? message.author} 的计划
      </span>
      <div className={cn("relative", !open && "max-h-48 overflow-hidden")}>
        <Markdown className="text-sm leading-6 [&_h1]:text-base [&_h2]:text-[15px] [&_h3]:text-sm">{message.content}</Markdown>
        {!open && <div className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-panel" />}
      </div>
      {long && (
        <button type="button" onClick={() => setOpen(!open)} className="self-start text-xs text-muted-foreground hover:text-foreground">
          {open ? "收起" : "展开全部"}
        </button>
      )}
    </section>
  );
}

/** A deliverable published with publish_file / publish_deliverable: open it in the viewer (hosted) or its link. */
export function ArtifactCard({ message, pid }: { message: MessageView; pid: string }) {
  const d = message.meta.deliverable;
  if (!d) return null;
  const item = { kind: d.kind, detail: { hosted: d.hosted, entry: d.entry } } as Pick<DeliverableView, "kind" | "detail">;
  const Icon = FILE_ICONS[fileIcon(item)];
  const look = deliverableKind(d.kind);
  const body = (
    <>
      <span className="flex size-10 shrink-0 items-center justify-center rounded-xl" style={{ background: look.bg, color: look.fg }}>
        <Icon aria-hidden className="size-5" strokeWidth={1.7} />
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm font-medium">{d.title}</span>
        <span className="text-xs text-muted-foreground">
          {look.label}
          {d.version ? ` · 第 ${d.version} 版` : ""} · {message.meta.employee ?? message.author} 发布
        </span>
      </span>
      <ExternalLink aria-hidden className="size-4 shrink-0 text-muted-foreground" strokeWidth={1.8} />
    </>
  );
  const cls = cn(CARD, "flex max-w-[420px] items-center gap-3 px-3 py-2.5 hover:border-input");
  if (d.hosted)
    return (
      <Link to={`/projects/${pid}/deliverables/${d.id}`} className={cls} aria-label={`打开交付物 ${d.title}`}>
        {body}
      </Link>
    );
  return (
    <a href={safeHref(d.url)} target="_blank" rel="noreferrer" className={cls} aria-label={`打开交付物 ${d.title}`}>
      {body}
    </a>
  );
}

/** The quiet note left when a session was rebuilt from a recap. */
export function ResumedNote({ message }: { message: MessageView }) {
  return (
    <p className="flex items-center justify-center gap-2 text-xs text-muted-foreground" title={`${message.meta.employee ?? message.author} 的会话记录丢失，平台用对话摘要开了新会话`}>
      <span className="h-px w-8 bg-[#E7E4DD]" />
      会话已恢复
      <span className="h-px w-8 bg-[#E7E4DD]" />
    </p>
  );
}
