// Project icons (ProjectView.icon): '' = the name's initial on its stable tint,
// "color:#RRGGBB" = the initial on that color, anything else = an emoji.
import { projectTint } from "./present";

export type IconLook = { kind: "emoji"; emoji: string } | { kind: "initial"; letter: string; bg: string; fg: string };

export const ICON_COLORS = ["#2E7D4F", "#1F6FD1", "#7A4FD1", "#C8622A", "#B7413E", "#2F8F9D", "#C79A1E", "#4A4843"];

export const ICON_EMOJIS = ["🚀", "🧭", "🛠️", "📦", "🧪", "📈", "🎨", "📝", "💡", "🔧", "🌐", "🤖", "📱", "🗂️", "⚡", "🔒", "🧩", "🎯", "📚", "🏗️"];

const COLOR = /^color:(#[0-9A-Fa-f]{6})$/;

export function initialOf(name: string): string {
  return (Array.from(name.trim())[0] ?? "?").toUpperCase();
}

export function iconLook(icon: string | undefined, name: string): IconLook {
  const value = (icon ?? "").trim();
  const color = value.match(COLOR);
  if (color) return { kind: "initial", letter: initialOf(name), bg: `${color[1]}1F`, fg: color[1] };
  if (value && !value.startsWith("color:")) return { kind: "emoji", emoji: value };
  const [bg, fg] = projectTint(name);
  return { kind: "initial", letter: initialOf(name), bg, fg };
}

export const colorIcon = (hex: string) => `color:${hex}`;
