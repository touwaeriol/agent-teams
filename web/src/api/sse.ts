// EventSource with resume: the browser resends Last-Event-ID on its own
// reconnects; when we open a new EventSource (first connect after a snapshot,
// or after the browser gave up) the last seen id goes in `?last_event_id=`
// because EventSource cannot set headers.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
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
  url: string | null;
  events: readonly string[];
  onEvent: (type: string, data: unknown, id: string) => void;
  /** Resume position for the first connection (e.g. a snapshot's last_event_id). */
  startId?: string | null;
  params?: Record<string, string>;
}

export function useEventStream({ url, events, onEvent, startId = null, params }: StreamOptions): StreamState {
  const [state, setState] = useState<StreamState>("connecting");
  const handler = useRef(onEvent);
  handler.current = onEvent;
  const paramKey = JSON.stringify(params ?? {});
  useEffect(() => {
    if (!url || typeof EventSource === "undefined") return;
    let lastId = startId;
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;
    const receive = (type: string) => (e: MessageEvent) => {
      if (e.lastEventId) lastId = e.lastEventId;
      handler.current(type, parse(e.data), e.lastEventId);
    };
    const open = () => {
      source = new EventSource(streamUrl(url, lastId, JSON.parse(paramKey)));
      source.onopen = () => {
        attempt = 0;
        setState("live");
      };
      source.onerror = () => {
        if (source?.readyState !== EventSource.CLOSED) return setState("connecting");
        setState("down");
        if (!closed) timer = setTimeout(open, backoff(attempt++));
      };
      events.forEach((name) => source!.addEventListener(name, receive(name) as EventListener));
    };
    open();
    return () => {
      closed = true;
      clearTimeout(timer);
      source?.close();
    };
  }, [url, events, startId, paramKey]);
  return state;
}

function parse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

/** The project stream: every frame updates the query cache (events.ts). */
export function useProjectEvents(pid: string | undefined): StreamState {
  const qc = useQueryClient();
  return useEventStream({
    url: pid ? `/api/projects/${pid}/events` : null,
    events: PROJECT_EVENTS,
    onEvent: (type, data) => applyProjectEvent(qc, pid!, type, data),
  });
}

/** The user stream (sidebar counts, nodes, notices). */
export function useUserEvents(enabled: boolean): StreamState {
  const qc = useQueryClient();
  return useEventStream({
    url: enabled ? "/api/events" : null,
    events: USER_EVENTS,
    onEvent: (type, data) => {
      const notice = applyUserEvent(qc, type, data);
      if (notice) toast(notice);
    },
  });
}
