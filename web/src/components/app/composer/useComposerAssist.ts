// Composer assist: the "/" and "@" popups of a message box, the @ references a message carries,
// and the platform commands (/status, /context, …) the web runs itself. Composer.vue creates one
// per box from its text and input element; options null switch everything off.
import { useQuery } from "@tanstack/vue-query";
import { computed, nextTick, ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";
import { useRouter } from "vue-router";
import { client, unwrap } from "@/api/client";
import {
  applyPick,
  commandOf,
  findTrigger,
  mentionItems,
  moveIndex,
  refsInText,
  refToken,
  slashItems,
  type AssistItem,
  type MessageRef,
  type Scope,
  type Trigger,
} from "./assist";

export interface AssistOptions {
  pid: string;
  /** The conversation (coordinator conversation or thread id); "" until known. */
  cid: string;
  scope: Scope;
}

/** A platform command's answer shown above the box. */
export type Panel = "effort" | "status" | "context" | "skills";

const PANELS: Panel[] = ["effort", "status", "context", "skills"];
const ROUTES: Record<string, string> = { usage: "usage", memory: "memory", config: "general" };

type Field = HTMLInputElement | HTMLTextAreaElement | null;
type Picked = Array<MessageRef & { label: string }>;

/** The @ references of each conversation's box, kept for the page so a remounted box gets them back. */
const pickedShelf = new Map<string, Ref<Picked>>();

export function useComposerAssist(options: MaybeRefOrGetter<AssistOptions | undefined>, text: Ref<string>, fieldOf: () => Field) {
  const router = useRouter();
  const opts = computed(() => toValue(options));
  const enabled = computed(() => Boolean(opts.value?.pid));
  const trigger = ref<Trigger | null>(null);
  const active = ref(0);
  const dismissed = ref<string | null>(null);
  const panel = ref<Panel | null>(null);
  const ownPicked = ref<Picked>([]);
  const pickedBox = (): Ref<Picked> => {
    const o = opts.value;
    if (!o?.pid || !o.cid) return ownPicked;
    const k = `${o.scope}:${o.pid}:${o.cid}`;
    let r = pickedShelf.get(k);
    if (!r) pickedShelf.set(k, (r = ref<Picked>([])));
    return r;
  };
  const picked = computed<Picked>({ get: () => pickedBox().value, set: (v) => (pickedBox().value = v) });
  const query = ref("");
  let debounce: ReturnType<typeof setTimeout> | null = null;

  const slashOpen = computed(() => trigger.value?.kind === "slash");
  const mentionOpen = computed(() => trigger.value?.kind === "mention");

  const commands = useQuery({
    queryKey: computed(() => ["c", opts.value?.cid ?? "", "commands"]),
    queryFn: () => unwrap(client.GET("/api/conversations/{cid}/commands", { params: { path: { cid: opts.value!.cid } } })),
    enabled: computed(() => Boolean(opts.value?.cid) && (slashOpen.value || panel.value === "skills")),
    staleTime: 30_000,
  });
  const refs = useQuery({
    queryKey: computed(() => ["p", opts.value?.pid ?? "", "references", query.value]),
    queryFn: () => unwrap(client.GET("/api/projects/{pid}/references", { params: { path: { pid: opts.value!.pid }, query: { q: query.value } } })),
    enabled: computed(() => enabled.value && mentionOpen.value),
    staleTime: 60_000,
    placeholderData: (prev) => prev,
  });

  const items = computed<AssistItem[]>(() => {
    const t = trigger.value;
    if (!t || !opts.value) return [];
    const d = commands.data.value;
    if (t.kind === "slash") return slashItems(t.query, opts.value.scope, d?.commands ?? [], { hidden: d?.hidden ?? [], custom: d?.custom ?? [] });
    return mentionItems(refs.data.value?.items ?? []);
  });
  const loading = computed(() => (slashOpen.value ? commands.isFetching.value && !commands.data.value : mentionOpen.value && refs.isFetching.value && !refs.data.value));
  const note = computed(() => (mentionOpen.value ? refs.data.value?.files_note ?? "" : ""));
  const open = computed(() => Boolean(trigger.value));

  function caret(): number {
    return fieldOf()?.selectionStart ?? text.value.length;
  }

  /** Recomputes the trigger from the text and caret (after typing or moving the caret). */
  function refresh() {
    if (!enabled.value) return void (trigger.value = null);
    const t = findTrigger(text.value, caret());
    const key = t ? `${t.kind}:${t.start}` : null;
    if (key && key === dismissed.value) return void (trigger.value = null);
    dismissed.value = null;
    if (t?.kind !== trigger.value?.kind || t?.query !== trigger.value?.query) active.value = 0;
    trigger.value = t;
    if (t?.kind === "mention") {
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(() => (query.value = t.query), 150);
    }
  }

  watch(text, (value) => {
    // A reference goes away with its token: deleting "@path" from the text drops the chip.
    if (picked.value.length) picked.value = picked.value.filter((r) => value.includes(refToken(r)));
    void nextTick(refresh);
  });

  function close() {
    if (trigger.value) dismissed.value = `${trigger.value.kind}:${trigger.value.start}`;
    trigger.value = null;
  }

  async function setText(next: string, at: number) {
    text.value = next;
    await nextTick();
    const el = fieldOf();
    el?.focus();
    el?.setSelectionRange(at, at);
    refresh();
  }

  /** Runs a platform command: settings pages open, the rest show their panel. */
  function runCommand(name: string) {
    const o = opts.value;
    if (!o) return;
    if (ROUTES[name]) return void router.push(`/projects/${o.pid}/settings/${ROUTES[name]}`);
    if (PANELS.includes(name as Panel)) panel.value = name as Panel;
  }

  async function choose(item: AssistItem | undefined = items.value[active.value]) {
    const t = trigger.value;
    if (!item || !t) return;
    if (item.command) {
      const after = applyPick(text.value, t, "");
      trigger.value = null;
      await setText(after.text, after.caret);
      runCommand(item.command);
      return;
    }
    if (item.ref) picked.value = [...picked.value, item.ref];
    const next = applyPick(text.value, t, item.insert ?? "");
    trigger.value = null;
    await setText(next.text, next.caret);
  }

  const keys: Record<string, () => void> = {
    ArrowDown: () => (active.value = moveIndex(active.value, 1, items.value.length)),
    ArrowUp: () => (active.value = moveIndex(active.value, -1, items.value.length)),
    Enter: () => void choose(),
    Tab: () => void choose(),
    Escape: close,
  };

  /** Popup keys (true = handled, the box must not act on it). */
  function onKeyDown(e: KeyboardEvent): boolean {
    if (!trigger.value || e.isComposing) return false;
    const act = keys[e.key];
    if (!act || e.shiftKey || ((e.key === "Enter" || e.key === "Tab") && items.value.length === 0)) return false;
    e.preventDefault();
    act();
    return true;
  }

  /** "/" from the + menu: starts the message with a slash and opens the list. */
  async function startSlash() {
    const body = text.value.replace(/^\/\S*\s?/, "");
    await setText(`/${body ? ` ${body}` : ""}`, 1);
  }

  /** "@" from the + menu: inserts an @ at the caret. */
  async function startMention() {
    const at = caret();
    const before = text.value.slice(0, at);
    const lead = before && !/\s$/.test(before) ? " @" : "@";
    await setText(before + lead + text.value.slice(at), at + lead.length);
  }

  return {
    enabled,
    open,
    trigger,
    items,
    active,
    loading,
    note,
    panel,
    picked,
    commands,
    choose,
    close,
    onKeyDown,
    refresh,
    runCommand,
    startSlash,
    startMention,
    closePanel: () => (panel.value = null),
    highlight: (i: number) => (active.value = i),
    /** A whole message that is a platform command runs here instead of being sent. */
    intercept(value: string): boolean {
      const o = opts.value;
      const name = o ? commandOf(value, o.scope, commands.data.value?.hidden ?? []) : null;
      if (!name) return false;
      runCommand(name);
      return true;
    },
    /** The references the message to send carries. */
    takeRefs: (value: string) => refsInText(value, picked.value),
    removeRef: (i: number) => (picked.value = picked.value.filter((_, j) => j !== i)),
    clearRefs: () => (picked.value = []),
  };
}

export type ComposerAssist = ReturnType<typeof useComposerAssist>;
