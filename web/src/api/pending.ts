// Messages the user just sent, shown in the chat before the server has them (optimistic sends).
//
// A send is kept here (not in the messages query cache, so a refetch cannot wipe it) until the
// POST returns: its MessageView then goes into the cache and the pending entry is dropped in the
// same tick. Chat.vue draws server messages followed by the still-pending ones (`withPending`).
//
// Deduplication: the SSE frame of a message (or a refetch) may arrive before the POST returns
// and carries no Idempotency-Key, so a pending entry also counts as delivered once the server
// list holds a user message with the same content and number of attachments that is newer than
// every message the chat had when it was sent (id not seen then, and a larger numeric id).
// Each server message confirms at most one entry, oldest first (sends on their way before failed
// ones), and a message one send got back never confirms another, so the same text sent twice
// shows twice. When the chat had not loaded yet at the time of the send there is nothing to tell
// old from new, so only the POST's reply confirms it; if the cache is still empty then, the
// stored message is drawn from here until the cache has it.
// A failed send stays (red, 发送失败 · 重试); a retry reuses its Idempotency-Key, so a request
// that did reach the server is not stored twice. A failed send the server turns out to have
// (the same text sent again from the box, or a reply lost on the way) is dropped after the next
// successful send of the conversation.
//
// Sends to one conversation go out one after another, so the server stores them in order.
import { useQueryClient, type QueryClient, type QueryKey } from "@tanstack/vue-query";
import { reactive } from "vue";
import { dropPickedRefs } from "@/components/app/composer/useComposerAssist";
import { clearDraftIf } from "@/lib/persist";
import { toast } from "@/lib/toast";
import { conversations, idempotencyKey } from "./actions";
import { appendMessage } from "./events";
import { errorText } from "./mutations";
import { flattenMessages, keys, type MessagePages } from "./queries";
import type { AttachmentRef, MessageRefChip, MessageView } from "./views";
import type { components } from "./schema";

type MessageRef = components["schemas"]["MessageRef"];

export type PendingStatus = "sending" | "failed";

export interface PendingMessage {
  key: string;
  cid: string;
  content: string;
  /** Uploaded attachment ids (what the POST carries). */
  images: string[];
  refs: MessageRef[];
  /** How the attachments are drawn meanwhile. */
  attachments: AttachmentRef[];
  status: PendingStatus;
  error?: string;
  at: string;
  /** Message ids the chat had when it was sent (plus those other sends got back), and the largest numeric one. */
  seen: string[];
  seenMax: number;
  /** The chat had not loaded when it was sent: only the POST's reply confirms it. */
  blind?: boolean;
  /** Delivered while the cache could not take the message: drawn from here until it can. */
  sent?: MessageView;
}

export interface SendInput {
  cid: string;
  content: string;
  images?: string[];
  refs?: MessageRef[];
  attachments?: AttachmentRef[];
  /** More queries to refresh after a successful send (the messages are always refreshed). */
  invalidate?: () => QueryKey[];
  /** Called after a retry from the chat succeeds (the box clears the draft it put back). */
  onRetried?: (p: PendingMessage) => void;
}

const store = reactive(new Map<string, PendingMessage[]>());
// Not reactive: what a retry needs besides the entry itself.
const extras = new Map<string, Pick<SendInput, "invalidate" | "onRetried">>();
const queues = new Map<string, Promise<unknown>>();

export const TEMP_PREFIX = "tmp-";
export const tempId = (key: string) => `${TEMP_PREFIX}${key}`;

/** The pending sends of a conversation, oldest first (reactive). */
export function pendingOf(cid: string | undefined): PendingMessage[] {
  return (cid && store.get(cid)) || [];
}

export function findPending(key: string): PendingMessage | undefined {
  for (const list of store.values()) {
    const p = list.find((x) => x.key === key);
    if (p) return p;
  }
  return undefined;
}

function patch(key: string, change: Partial<PendingMessage>) {
  for (const [cid, list] of store) if (list.some((x) => x.key === key)) store.set(cid, list.map((x) => (x.key === key ? { ...x, ...change } : x)));
}

/** Drops a pending send (delivered, or a failed one the user deleted). */
export function dropPending(key: string): void {
  for (const [cid, list] of store) {
    if (!list.some((x) => x.key === key)) continue;
    const rest = list.filter((x) => x.key !== key);
    if (rest.length) store.set(cid, rest);
    else store.delete(cid);
  }
  extras.delete(key);
}

/** Forgets every pending send (tests). */
export function resetPending(): void {
  store.clear();
  extras.clear();
  queues.clear();
}

const numeric = (id: string) => (/^\d+$/.test(id) ? Number(id) : NaN);

/** Does server message m stand for pending send p (see the top of the file)? */
export function confirms(m: MessageView, p: PendingMessage): boolean {
  if (p.blind || p.sent || m.role !== "user" || m.content !== p.content) return false;
  if ((m.meta?.attachments?.length ?? 0) !== p.images.length) return false;
  if (p.seen.includes(m.id)) return false;
  const n = numeric(m.id);
  return Number.isNaN(n) || !Number.isFinite(p.seenMax) || n > p.seenMax;
}

const chipLabel = (r: MessageRef) => (r.kind === "thread" && r.no ? `#${r.no}` : (r.path?.split("/").pop() ?? r.name ?? r.id ?? r.kind));

/** How a pending send is drawn: a user message with a temporary id and `local` set. */
export function pendingView(p: PendingMessage): MessageView {
  const meta: MessageView["meta"] = {};
  if (p.attachments.length) meta.attachments = p.attachments;
  if (p.refs.length) meta.refs = p.refs.map((r) => ({ ...r, label: chipLabel(r) }) as MessageRefChip);
  return { id: tempId(p.key), role: "user", author: "", kind: "chat", content: p.content, meta, created_at: p.at, local: { key: p.key, status: p.status, error: p.error } };
}

/** Keys of the pending sends that server messages confirm (one message each; sends on their way first). */
export function confirmedKeys(server: MessageView[], pending: PendingMessage[]): Set<string> {
  const used = new Set(pending.flatMap((p) => (p.sent ? [p.sent.id] : [])));
  const done = new Set<string>();
  const order = [...pending.filter((p) => p.status === "sending"), ...pending.filter((p) => p.status === "failed")];
  for (const p of order) {
    const m = server.find((x) => !used.has(x.id) && confirms(x, p));
    if (!m) continue;
    used.add(m.id);
    done.add(p.key);
  }
  return done;
}

/** The chat's messages: the server's, then the pending sends none of them confirms yet. */
export function withPending(server: MessageView[], pending: PendingMessage[]): MessageView[] {
  if (pending.length === 0) return server;
  const ids = new Set(server.map((m) => m.id));
  const done = confirmedKeys(server, pending);
  const shown: MessageView[] = [];
  for (const p of pending) {
    if (p.sent) {
      if (!ids.has(p.sent.id)) shown.push(p.sent);
    } else if (!done.has(p.key)) shown.push(pendingView(p));
  }
  return shown.length ? [...server, ...shown] : server;
}

/** Drops delivered sends the chat's messages now hold (Chat.vue calls it when they change). */
export function prunePending(cid: string | undefined, server: MessageView[]): void {
  const list = pendingOf(cid).filter((p) => p.sent);
  if (list.length === 0) return;
  const ids = new Set(server.map((m) => m.id));
  list.filter((p) => ids.has(p.sent!.id)).forEach((p) => dropPending(p.key));
}

/** After p was delivered as m: other sends never take m for theirs; failed ones the server has go. */
function settleOthers(qc: QueryClient, p: PendingMessage, m: MessageView) {
  for (const q of pendingOf(p.cid)) if (q.key !== p.key && q.status === "sending" && !q.seen.includes(m.id)) patch(q.key, { seen: [...q.seen, m.id] });
  const server = flattenMessages(qc.getQueryData<MessagePages>(keys.messages(p.cid)));
  const rest = pendingOf(p.cid).filter((q) => q.key !== p.key);
  const done = confirmedKeys(server, rest);
  // The same text sent again from the box while it was failed: m stands for it too.
  const twin = rest.find((q) => q.status === "failed" && !done.has(q.key) && q.content === m.content && q.images.length === (m.meta?.attachments?.length ?? 0));
  if (twin && !server.some((x) => x.id === m.id)) done.add(twin.key);
  rest.filter((q) => q.status === "failed" && done.has(q.key)).forEach((q) => dropPending(q.key));
}

/** Posts a pending send (after the conversation's earlier sends) and settles it. */
function deliver(qc: QueryClient, key: string): Promise<MessageView> {
  const first = findPending(key);
  if (!first) return Promise.reject(new Error("消息已不在发送队列里"));
  const cid = first.cid;
  const run = (queues.get(cid) ?? Promise.resolve()).then(async () => {
    const p = findPending(key);
    if (!p) throw new Error("消息已删除");
    try {
      const m = (await conversations.send(p.cid, p.content, false, p.images, p.refs, p.key)) as MessageView;
      // Same tick: the real message takes the pending one's place without a flicker. A cache that
      // cannot take it yet (not loaded): the entry draws the message until the cache has it.
      qc.setQueryData<MessagePages>(keys.messages(cid), (data) => appendMessage(data, m));
      const more = extras.get(key)?.invalidate?.() ?? [];
      const held = flattenMessages(qc.getQueryData<MessagePages>(keys.messages(cid))).some((x) => x.id === m.id);
      settleOthers(qc, p, m);
      if (held) dropPending(key);
      else patch(key, { sent: m });
      for (const queryKey of [keys.messages(cid), ...more]) void qc.invalidateQueries({ queryKey });
      return m;
    } catch (e) {
      patch(key, { status: "failed", error: errorText(e) });
      throw e;
    }
  });
  queues.set(
    cid,
    run.catch(() => undefined),
  );
  return run;
}

/**
 * Shows the message in the chat at once and sends it. Resolves with the stored message; rejects
 * when the POST fails (the message then stays in the chat, marked failed, until retried or deleted).
 */
export function sendMessage(qc: QueryClient, input: SendInput): Promise<MessageView> {
  const key = idempotencyKey();
  const data = qc.getQueryData<MessagePages>(keys.messages(input.cid));
  const known = flattenMessages(data);
  const nums = known.map((m) => numeric(m.id)).filter((n) => !Number.isNaN(n));
  const p: PendingMessage = {
    key,
    cid: input.cid,
    content: input.content,
    images: input.images ?? [],
    refs: input.refs ?? [],
    attachments: input.attachments ?? [],
    status: "sending",
    at: new Date().toISOString(),
    seen: known.map((m) => m.id),
    seenMax: nums.length ? Math.max(...nums) : -Infinity,
    blind: !data?.pages.length,
  };
  store.set(input.cid, [...pendingOf(input.cid), p]);
  extras.set(key, { invalidate: input.invalidate, onRetried: input.onRetried });
  return deliver(qc, key);
}

/** Sends a failed message again with the same Idempotency-Key. */
export async function retryMessage(qc: QueryClient, key: string): Promise<MessageView | undefined> {
  const p = findPending(key);
  if (!p || p.status !== "failed") return undefined;
  const onRetried = extras.get(key)?.onRetried;
  patch(key, { status: "sending", error: undefined });
  const m = await deliver(qc, key);
  onRetried?.(p);
  return m;
}

/** What a message box hands over with a send: its uploaded attachments (see Composer's onSend). */
export interface SentAttachment {
  id?: string;
  name: string;
  kind: "image" | "pdf" | "text";
}

const CONTENT_TYPE = { image: "image/png", pdf: "application/pdf", text: "text/plain" } as const;

/** Attachments as the chat draws them while the message is pending (by id: the files are uploaded). */
export function attachmentRefs(items: SentAttachment[]): AttachmentRef[] {
  return items.filter((x) => x.id).map((x) => ({ id: x.id!, name: x.name, content_type: CONTENT_TYPE[x.kind], size: 0 }));
}

/** What useSendMessage clears from the box when a retry from the chat delivers a message it put back. */
export interface SendBox {
  /** The box's draft key (its text). */
  draft?: () => string | undefined;
  /** The box's attachments. */
  attachments?: { items: { value: Array<{ key: string; id?: string }> }; remove: (key: string) => void };
  /** Where the box keeps its @ references (useComposerAssist's scope, project and conversation). */
  assist?: () => { scope: string; pid: string; cid: string } | undefined;
}

/**
 * The onSend of a message box (coordinator, lead): the message shows in the chat at once; a
 * failure is a toast plus the red message in the chat, and rejects so the box puts the message
 * back (text, attachments, references). A later retry from the chat that succeeds takes them out
 * of the box again: the text if it is still untouched, the message's attachments, and its
 * references unless the text left in the box still names them.
 */
export function useSendMessage(cid: () => string, o: SendBox & { invalidate?: () => QueryKey[] } = {}) {
  const qc = useQueryClient();
  return (text: string, images: string[], refs: MessageRef[], taken: SentAttachment[] = []) => {
    const draft = o.draft?.();
    const where = o.assist?.();
    const onRetried = (p: PendingMessage) => {
      const left = draft ? clearDraftIf(draft, p.content) : "";
      const box = o.attachments;
      if (box) for (const x of box.items.value.filter((i) => i.id && p.images.includes(i.id))) box.remove(x.key);
      if (where) dropPickedRefs(where, p.refs, left);
    };
    return sendMessage(qc, { cid: cid(), content: text, images, refs, attachments: attachmentRefs(taken), invalidate: o.invalidate, onRetried }).catch((e: unknown) => {
      toast.error(errorText(e));
      throw e;
    });
  };
}
