// Keyboard shortcut helpers: platform labels (⌥↑ on a Mac, Alt+↑ elsewhere) and whether a key
// press happened while the user is typing.

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

/** "Alt+↑" → "⌥↑", "Ctrl+Alt+Enter" → "⌃⌥↩" on a Mac; unchanged elsewhere. */
export function keyLabel(keys: string, mac: boolean = isMac): string {
  if (!mac) return keys;
  const MAC: Record<string, string> = { Ctrl: "⌃", Alt: "⌥", Shift: "⇧", Enter: "↩" };
  return keys.split("+").map((k) => MAC[k] ?? k).join("");
}

/** The key press came from a text field (or editable element) that has text in it. */
export function typingIn(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el !== "object") return false;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) return el.value.length > 0;
  return Boolean(el.isContentEditable && el.textContent);
}

export type ThreadShortcut = "prev" | "next" | "resolve";

/** Alt+↑ / Alt+↓ (also while typing: Alt+arrows don't edit text) and Ctrl+Alt+Enter (⌃⌥↩, or ⌘⌥↩ on a Mac). */
export function threadShortcut(e: KeyboardEvent): ThreadShortcut | null {
  if (e.isComposing || e.shiftKey || !e.altKey) return null;
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) return "resolve";
  if (e.ctrlKey || e.metaKey) return null;
  if (e.key === "ArrowUp") return "prev";
  if (e.key === "ArrowDown") return "next";
  return null;
}
