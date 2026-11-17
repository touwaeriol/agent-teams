// Admin page helpers (pure).
export const USER_COLUMNS = ["账号", "角色", "节点", "进行中任务", "状态", "最近登录", "注册", ""];

/** A settings number box commits a whole number ≥ 0 that changed; anything else puts the saved value back. */
export function parseSetting(text: string, saved: number): number | null {
  const n = Number(text);
  return Number.isInteger(n) && n >= 0 && n !== saved ? n : null;
}

export function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString("zh-CN", { month: "long", day: "numeric" });
}

export function auditTime(iso: string): string {
  return new Date(iso).toLocaleString("zh-CN");
}
