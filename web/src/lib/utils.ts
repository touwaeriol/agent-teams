import { $t, intlLocale } from "@/i18n";
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function timeAgo(value: string | number): string {
  const at = typeof value === "number" ? value * 1000 : Date.parse(value);
  const s = Math.max(0, Math.round((Date.now() - at) / 1000));
  if (s < 60) return $t("刚刚");
  if (s < 3600) return $t("{n} 分钟前", { n: Math.floor(s / 60) });
  if (s < 86400) return $t("{n} 小时前", { n: Math.floor(s / 3600) });
  return $t("{n} 天前", { n: Math.floor(s / 86400) });
}

export function clock(value: string): string {
  return new Date(value).toLocaleTimeString(intlLocale(), { hour: "2-digit", minute: "2-digit" });
}
