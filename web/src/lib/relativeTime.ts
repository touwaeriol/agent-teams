// Short relative times for lists ("7分钟", "1天"), shared by the thread list and headers.

/** "刚刚" / "51分钟" / "3小时" / "1天" / "11月2日" (older than 30 days). */
export function shortAgo(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const m = Math.floor(Math.max(0, now - t) / 60_000);
  if (m < 1) return "刚刚";
  if (m < 60) return `${m}分钟`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}小时`;
  const d = Math.floor(h / 24);
  if (d <= 30) return `${d}天`;
  const date = new Date(t);
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}
