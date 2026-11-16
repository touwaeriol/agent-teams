// EventSource with resume: the browser resends Last-Event-ID on its own
// reconnects; when we open a new EventSource (first connect after a snapshot,
// or after the browser gave up) the last seen id goes in `?last_event_id=`
// because EventSource cannot set headers.
//
// `openEventStream` is the framework-free core (tested with a fake EventSource);
// `useEventStream` ties it to a component's lifetime and reactive URL.
import { useQueryClient } from "@tanstack/vue-query";
import { ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";
import { toast } from "@/lib/toast";
import { applyProjectEvent, applyUserEvent, PROJECT_EVENTS, USER_EVENTS } from "./events";

export type StreamState = "connecting" | "live" | "down";

export function streamUrl(base: string, lastId: string | null, extra: Record<string, string> = {}): string {
  const params = new URLSearchParams(extra);
  if (lastId) params.set("last_event_id", lastId);
  const qs = params.toString();
  return qs ? `${base}${base.includes("?") ? "&" : "?"}${qs}` : base;
}

/** Reconnect delay after the browser closed the stream: 1 s, 2 s, 4 s … capped at 30 s. */
export function backoff(attempt: number): number {
  return Math.min(30_000, 1000 * 2 ** Math.max(0, attempt));
}

export interface StreamOptions {
  url: string;
  events: readonly string[];
  onEvent: (type: string, data: unknown, id: string) => void;
  onState: (state: StreamState) => void;
  /** Resume position for the first connection (e.g. a snapshot's last_event_id). */
  startId?: string | null;
  params?: Record<string, string>;
  /** Injected in tests; defaults to the browser's EventSource. */
  create?: (url: string) => EventSource;
}

/** Opens the stream and keeps it open (with backoff) until the returned close() is called. */
export function openEventStream(o: StreamOptions): () => void {
  const create = o.create ?? ((u: string) => new EventSource(u));
  let lastId = o.startId ?? null;
  let source: EventSource | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let attempt = 0;
  let closed = false;
  const receive = (type: string) => (e: MessageEvent) => {
    if (e.lastEventId) lastId = e.lastEventId;
    o.onEvent(type, parse(e.data), e.lastEventId);
  };
  const open = () => {
    source = create(streamUrl(o.url, lastId, o.params));
    source.onopen = () => {
      attempt = 0;
      o.onState("live");
    };
    source.onerror = () => {
      if (source?.readyState !== 2) return o.onState("connecting");
      o.onState("down");
      if (!closed) timer = setTimeout(open, backoff(attempt++));
    };
    o.events.forEach((name) => source!.addEventListener(name, receive(name) as EventListener));
  };
  open();
  return () => {
    closed = true;
    clearTimeout(timer);
    source?.close();
  };
}

function parse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export interface UseStreamOptions {
  url: MaybeRefOrGetter<string | null>;
  events: readonly string[];
  onEvent: (type: string, data: unknown, id: string) => void;
  startId?: MaybeRefOrGetter<string | null>;
  params?: MaybeRefOrGetter<Record<string, string> | undefined>;
}

/** The stream for the current URL; reopens when the URL (or params) change, closes on unmount. */
export function useEventStream(o: UseStreamOptions): Ref<StreamState> {
  const state = ref<StreamState>("connecting");
  watch(
    () => [toValue(o.url), toValue(o.startId) ?? null, JSON.stringify(toValue(o.params) ?? {})] as const,
    ([url, startId, paramKey], _old, onCleanup) => {
      if (!url || typeof EventSource === "undefined") return;
      const close = openEventStream({
        url,
        events: o.events,
        startId,
        params: JSON.parse(paramKey),
        onEvent: o.onEvent,
        onState: (s) => (state.value = s),
      });
      onCleanup(close);
    },
    { immediate: true },
  );
  return state;
}

/** The project stream: every frame updates the query cache (events.ts). */
export function useProjectEvents(pid: MaybeRefOrGetter<string | undefined>): Ref<StreamState> {
  const qc = useQueryClient();
  return useEventStream({
    url: () => (toValue(pid) ? `/api/projects/${toValue(pid)}/events` : null),
    events: PROJECT_EVENTS,
    onEvent: (type, data) => applyProjectEvent(qc, toValue(pid)!, type, data),
  });
}

/** The user stream (sidebar counts, nodes, notices). */
export function useUserEvents(enabled: MaybeRefOrGetter<boolean>): Ref<StreamState> {
  const qc = useQueryClient();
  return useEventStream({
    url: () => (toValue(enabled) ? "/api/events" : null),
    events: USER_EVENTS,
    onEvent: (type, data) => {
      const notice = applyUserEvent(qc, type, data);
      if (notice) toast(notice);
    },
  });
}
