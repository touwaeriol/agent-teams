// Auto-growing textarea: the box follows its content from minRows lines up to a cap (a share of
// the viewport, with a pixel floor), then scrolls inside. The content is measured on a hidden copy
// of the textarea (same width, font, padding and wrapping), so the real box never collapses to
// `height: auto` mid-edit: its scroll position, the caret and the page around it stay put.
import { nextTick, onBeforeUnmount, onMounted, ref, toValue, watch, type MaybeRefOrGetter, type Ref } from "vue";

/** Default cap: 40% of the viewport, never under 160px (short windows still get ~6 lines). */
export const MAX_SHARE = 0.4;
export const MAX_FLOOR = 160;
export const defaultMaxHeight = () => Math.max(MAX_FLOOR, Math.round(window.innerHeight * MAX_SHARE));

/**
 * The scrolling area the box shares a column with (the chat above it): the nearest ancestor
 * child that scrolls vertically, grows (flex) and does not contain the box.
 */
export function neighbourScroller(el: HTMLElement): HTMLElement | null {
  for (let node = el.parentElement; node && node !== document.body; node = node.parentElement) {
    for (const child of Array.from(node.children) as HTMLElement[]) {
      if (child.contains(el)) continue;
      const cs = window.getComputedStyle(child);
      if (/(auto|scroll)/.test(cs.overflowY) && parseFloat(cs.flexGrow) > 0) return child;
    }
  }
  return null;
}

export interface AutoGrowOptions {
  minRows?: number;
  /** Largest height in px (border box); read on every measure so it follows the window. */
  maxHeight?: () => number;
  /**
   * Keep at least this many px of the neighbouring scroll area (the chat) visible: on short
   * windows, with attachments or a plan card, the cap comes down so the chat is never squeezed out.
   */
  keepVisible?: number;
  /** Called after the box was resized (e.g. to re-sync an overlay with the new scroll position). */
  onResize?: (el: HTMLTextAreaElement) => void;
}

export interface AutoGrow {
  /** Current height in px (0 until measured). */
  height: Readonly<Ref<number>>;
  /** Content is taller than the cap: the box scrolls. */
  scrolling: Readonly<Ref<boolean>>;
  /** Width the vertical scrollbar takes from the text (0 when not scrolling). */
  gutter: Readonly<Ref<number>>;
  update(): void;
}

// Everything that changes how text wraps or how tall a line is.
const COPIED = [
  "boxSizing",
  "width",
  "fontFamily",
  "fontSize",
  "fontWeight",
  "fontStyle",
  "fontVariant",
  "fontStretch",
  "fontFeatureSettings",
  "letterSpacing",
  "wordSpacing",
  "lineHeight",
  "textTransform",
  "textIndent",
  "tabSize",
  "whiteSpace",
  "wordBreak",
  "overflowWrap",
  "paddingTop",
  "paddingRight",
  "paddingBottom",
  "paddingLeft",
  "borderTopWidth",
  "borderRightWidth",
  "borderBottomWidth",
  "borderLeftWidth",
  "borderStyle",
] as const;

const px = (v: string) => parseFloat(v) || 0;

type Field = HTMLInputElement | HTMLTextAreaElement | null;

export function useAutoGrow(field: Ref<Field>, value: MaybeRefOrGetter<string>, opts: AutoGrowOptions = {}): AutoGrow {
  const minRows = opts.minRows ?? 2;
  const maxHeight = opts.maxHeight ?? defaultMaxHeight;
  const height = ref(0);
  const scrolling = ref(false);
  const gutter = ref(0);
  let probe: HTMLTextAreaElement | null = null;
  let lastWidth = -1;

  function getProbe(): HTMLTextAreaElement {
    if (probe) return probe;
    probe = document.createElement("textarea");
    probe.setAttribute("aria-hidden", "true");
    probe.tabIndex = -1;
    probe.rows = 1;
    const s = probe.style;
    s.position = "absolute";
    s.top = "0";
    s.left = "-9999px";
    s.visibility = "hidden";
    s.pointerEvents = "none";
    s.overflow = "hidden";
    s.height = "0";
    s.minHeight = "0";
    s.maxHeight = "none";
    document.body.appendChild(probe);
    return probe;
  }

  /** Height of `text` laid out in the probe, as a border-box height. */
  function measure(p: HTMLTextAreaElement, text: string, borderY: number): number {
    p.value = text;
    return p.scrollHeight + borderY;
  }

  // Box metrics from the last measure (the caret check reuses them).
  let metrics = { borderY: 0, padTop: 0, padBottom: 0, line: 20 };
  // The chat this box shares a column with, and the room (box + chat) the cap was computed from.
  let scroller: HTMLElement | null = null;
  let room = -1;

  function update() {
    const el = field.value;
    if (!el || el.tagName !== "TEXTAREA") return;
    const ta = el as HTMLTextAreaElement;
    const width = ta.offsetWidth;
    if (!width) return; // hidden: nothing to measure against
    lastWidth = width;
    const cs = window.getComputedStyle(ta);
    const p = getProbe();
    for (const k of COPIED) p.style[k] = cs[k];
    p.style.width = `${width}px`;
    const borderY = px(cs.borderTopWidth) + px(cs.borderBottomWidth);
    const padTop = px(cs.paddingTop);
    const padBottom = px(cs.paddingBottom);
    const line = cs.lineHeight === "normal" || !px(cs.lineHeight) ? px(cs.fontSize) * 1.2 || 20 : px(cs.lineHeight);
    metrics = { borderY, padTop, padBottom, line };
    const min = Math.ceil(line * minRows + padTop + padBottom + borderY);
    let cap = maxHeight();
    if (opts.keepVisible) {
      watchScroller(neighbourScroller(ta));
      if (scroller) {
        room = ta.offsetHeight + scroller.clientHeight;
        cap = Math.min(cap, room - opts.keepVisible);
      }
    }
    const max = Math.max(min, Math.floor(cap));
    const text = toValue(value);
    const content = measure(p, text, borderY);
    const next = Math.min(Math.max(content, min), max);
    const over = content > max + 0.5;
    const changed = next !== height.value || over !== scrolling.value;
    if (changed) {
      ta.style.height = `${next}px`;
      ta.style.overflowY = over ? "auto" : "hidden";
      height.value = next;
      scrolling.value = over;
    }
    gutter.value = over ? Math.max(0, ta.offsetWidth - ta.clientWidth - px(cs.borderLeftWidth) - px(cs.borderRightWidth)) : 0;
    if (!changed) return;
    // The browser scrolled the caret into view against the old height; redo it for the new one.
    if (document.activeElement === ta) followCaret();
    opts.onResize?.(ta);
  }

  /**
   * Scrolls the caret's line into view when the box scrolls inside. Runs after the text changed
   * (typing, paste, dictation: setSelectionRange alone does not scroll in Chromium) and after a
   * resize of the focused box; never on its own, so reading further up is left alone.
   */
  function followCaret() {
    const ta = field.value as HTMLTextAreaElement | null;
    if (!ta || ta.tagName !== "TEXTAREA" || !scrolling.value || !probe) return;
    const { borderY, padTop, padBottom, line } = metrics;
    const text = toValue(value);
    const end = Math.min(ta.selectionEnd ?? text.length, text.length);
    const caretBottom = measure(probe, text.slice(0, end), borderY) - borderY - padBottom;
    const caretTop = caretBottom - line;
    const before = ta.scrollTop;
    if (caretBottom > ta.scrollTop + ta.clientHeight - padBottom) ta.scrollTop = caretBottom - ta.clientHeight + padBottom;
    else if (caretTop < ta.scrollTop + padTop) ta.scrollTop = Math.max(0, caretTop - padTop);
    if (ta.scrollTop !== before) opts.onResize?.(ta);
  }

  // Width changes re-wrap the text; our own height changes are ignored.
  let observer: ResizeObserver | null = null;
  // The chat changing height for its own reasons (plan card, chips, a banner) moves the cap. When
  // the box itself grows the chat shrinks by as much: box + chat stays the same, nothing to redo.
  let chatObserver: ResizeObserver | null = null;
  function watchScroller(next: HTMLElement | null) {
    if (next === scroller) return;
    chatObserver?.disconnect();
    scroller = next;
    room = -1;
    if (!next || typeof ResizeObserver === "undefined") return;
    chatObserver = new ResizeObserver(() => {
      const ta = field.value;
      if (!ta || !scroller) return;
      if (ta.offsetHeight + scroller.clientHeight !== room) update();
    });
    chatObserver.observe(next);
  }
  const onWindow = () => update();

  watch(
    () => toValue(value),
    () => {
      update();
      // After the flush: the caret placed by the writer (e.g. dictation's nextTick) is in by then.
      void nextTick(followCaret);
    },
    { flush: "post" },
  );
  watch(
    field,
    (el, _, onCleanup) => {
      observer?.disconnect();
      if (!el || el.tagName !== "TEXTAREA") return;
      update();
      if (typeof ResizeObserver !== "undefined") {
        observer = new ResizeObserver(() => {
          if (el.offsetWidth !== lastWidth) update();
        });
        observer.observe(el);
        onCleanup(() => observer?.disconnect());
      }
    },
    { flush: "post" },
  );

  onMounted(() => {
    window.addEventListener("resize", onWindow);
    void document.fonts?.ready.then(update);
  });
  onBeforeUnmount(() => {
    window.removeEventListener("resize", onWindow);
    observer?.disconnect();
    chatObserver?.disconnect();
    probe?.remove();
    probe = null;
  });

  return { height, scrolling, gutter, update };
}
