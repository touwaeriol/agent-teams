// Layout hot reload (office-layout.md §6). `useHotLayout` takes any new layout
// object (Vite HMR import, admin editor, a fresh GET after an SSE `layout`
// event), validates it, and keeps the last good layout when it is invalid.
// <Office> recompiles in place when the returned object changes, so people
// keep their state and position.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BUILTIN_LAYOUT } from "./builtin";
import { LayoutError, loadLayout, validateLayout } from "./load";
import type { OfficeLayout } from "./types";

export interface HotLayout {
  layout: OfficeLayout;
  /** Why the latest document was rejected (the previous good layout stays in use). */
  error?: string;
  warnings: string[];
}

export function useHotLayout(doc: unknown): HotLayout {
  const lastGood = useRef<OfficeLayout | null>(null);
  return useMemo(() => {
    try {
      const { layout, warnings } = validateLayout(doc);
      lastGood.current = layout;
      return { layout, warnings };
    } catch (e) {
      return { layout: lastGood.current ?? BUILTIN_LAYOUT, error: errorText(e), warnings: [] };
    }
  }, [doc]);
}

function errorText(e: unknown): string {
  return e instanceof LayoutError || e instanceof Error ? e.message : String(e);
}

export interface RemoteLayout extends HotLayout {
  loading: boolean;
  /** Re-fetch (with If-None-Match): call it on the project SSE `layout` event. */
  reload: () => void;
}

/** GET /api/office/layouts/{id} (+ extends), validated, hot-reloadable. */
export function useRemoteLayout(id?: string): RemoteLayout {
  const [doc, setDoc] = useState<unknown>(null);
  const [fetchError, setFetchError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);
  useEffect(() => {
    let live = true;
    setLoading(true);
    loadLayout(id)
      .then((r) => live && (setDoc(r.layout), setFetchError(undefined)))
      .catch((e) => live && setFetchError(errorText(e)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [id, nonce]);
  const hot = useHotLayout(doc ?? BUILTIN_LAYOUT);
  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { ...hot, error: fetchError ?? (doc ? hot.error : undefined), loading, reload };
}
