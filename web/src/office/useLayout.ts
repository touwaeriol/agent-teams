// Layout hot reload (office-layout.md §6). `useHotLayout` takes any new layout
// object (Vite HMR import, admin editor, a fresh GET after an SSE `layout`
// event), validates it, and keeps the last good one when it is invalid.
// <Office> recompiles in place when the returned layout changes, so people
// keep their state and position.
import { computed, ref, toValue, watch, type ComputedRef, type MaybeRefOrGetter, type Ref } from "vue";
import { BUILTIN_LAYOUT } from "./builtin";
import { LayoutError, loadLayout, validateLayout } from "./load";
import type { OfficeLayout } from "./types";

export interface HotLayout {
  layout: OfficeLayout;
  /** Why the latest document was rejected (the previous good layout stays in use). */
  error?: string;
  warnings: string[];
}

function errorText(e: unknown): string {
  return e instanceof LayoutError || e instanceof Error ? e.message : String(e);
}

/** Validates a layout document; `lastGood` (held by the caller) is used when it is invalid. */
export function hotLayout(doc: unknown, lastGood: { current: OfficeLayout | null }): HotLayout {
  try {
    const { layout, warnings } = validateLayout(doc);
    lastGood.current = layout;
    return { layout, warnings };
  } catch (e) {
    return { layout: lastGood.current ?? BUILTIN_LAYOUT, error: errorText(e), warnings: [] };
  }
}

export function useHotLayout(doc: MaybeRefOrGetter<unknown>): ComputedRef<HotLayout> {
  const lastGood = { current: null as OfficeLayout | null };
  return computed(() => hotLayout(toValue(doc), lastGood));
}

export interface RemoteLayout {
  layout: ComputedRef<OfficeLayout>;
  error: ComputedRef<string | undefined>;
  warnings: ComputedRef<string[]>;
  loading: Ref<boolean>;
  /** Re-fetch (with If-None-Match): call it on the project SSE `layout` event. */
  reload: () => void;
}

/** GET /api/office/layouts/{id} (+ extends), validated, hot-reloadable. */
export function useRemoteLayout(id?: MaybeRefOrGetter<string | undefined>): RemoteLayout {
  const doc = ref<unknown>(null);
  const fetchError = ref<string>();
  const loading = ref(true);
  const nonce = ref(0);
  watch(
    () => [toValue(id), nonce.value] as const,
    ([layoutId], _old, onCleanup) => {
      let live = true;
      onCleanup(() => (live = false));
      loading.value = true;
      loadLayout(layoutId)
        .then((r) => {
          if (!live) return;
          doc.value = r.layout;
          fetchError.value = undefined;
        })
        .catch((e) => live && (fetchError.value = errorText(e)))
        .finally(() => live && (loading.value = false));
    },
    { immediate: true },
  );
  const hot = useHotLayout(() => doc.value ?? BUILTIN_LAYOUT);
  return {
    layout: computed(() => hot.value.layout),
    error: computed(() => fetchError.value ?? (doc.value ? hot.value.error : undefined)),
    warnings: computed(() => hot.value.warnings),
    loading,
    reload: () => (nonce.value += 1),
  };
}
