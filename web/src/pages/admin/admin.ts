// Admin page helpers (pure).
import { ApiError } from "@/api/client";
import type { components } from "@/api/schema";

export const USER_COLUMNS = ["账号", "显示名", "角色", "节点", "进行中任务", "状态", "最近登录", "注册", ""];

/** Same as the server's auth.MinPasswordLength and emailPattern. */
export const MIN_PASSWORD_LENGTH = 8;
const EMAIL_PATTERN = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

export type CreateUserBody = components["schemas"]["AdminCreateUserInputBody"];

/** The 新建用户 form. `passwordMode` generated = the server makes up a temporary password. */
export interface NewUserForm {
  email: string;
  displayName: string;
  role: "admin" | "user";
  passwordMode: "generated" | "manual";
  password: string;
  mustChangePassword: boolean;
}

export const emptyNewUserForm = (): NewUserForm => ({
  email: "",
  displayName: "",
  role: "user",
  passwordMode: "generated",
  password: "",
  mustChangePassword: true,
});

export interface FormProblem {
  field: "email" | "password" | "display_name" | "";
  message: string;
}

/** What is wrong with the form before it is sent, or null. */
export function newUserProblem(f: NewUserForm): FormProblem | null {
  const email = f.email.trim().toLowerCase();
  if (!email) return { field: "email", message: "请填写邮箱" };
  if (!EMAIL_PATTERN.test(email)) return { field: "email", message: "请输入有效的邮箱地址" };
  if (f.displayName.trim().length > 100) return { field: "display_name", message: "显示名最多 100 个字" };
  if (f.passwordMode === "manual" && [...f.password].length < MIN_PASSWORD_LENGTH) {
    return { field: "password", message: `密码至少 ${MIN_PASSWORD_LENGTH} 位` };
  }
  return null;
}

export function newUserBody(f: NewUserForm): CreateUserBody {
  const body: CreateUserBody = { email: f.email.trim(), role: f.role, must_change_password: f.mustChangePassword };
  const name = f.displayName.trim();
  if (name) body.display_name = name;
  if (f.passwordMode === "manual") body.password = f.password;
  return body;
}

/** The server's refusal shown on the field it is about. */
export function createUserError(e: unknown): FormProblem {
  if (!(e instanceof ApiError)) return { field: "", message: e instanceof Error ? e.message : String(e) };
  if (e.code === "email_taken") return { field: "email", message: "这个邮箱已经有账号了" };
  if (e.code === "invalid_email") return { field: "email", message: e.message };
  if (e.code === "weak_password") return { field: "password", message: e.message };
  const f = e.fields[0]?.path ?? "";
  const field = f.endsWith("email") ? "email" : f.endsWith("password") ? "password" : f.endsWith("display_name") ? "display_name" : "";
  return { field, message: e.message };
}

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

/** 每 15 秒 / 每 5 分钟 / 每 2 小时 / 每天. */
export function intervalText(seconds: number): string {
  if (seconds > 0 && seconds % 86400 === 0) return seconds === 86400 ? "每天" : `每 ${seconds / 86400} 天`;
  if (seconds >= 3600 && seconds % 3600 === 0) return `每 ${seconds / 3600} 小时`;
  if (seconds >= 60 && seconds % 60 === 0) return `每 ${seconds / 60} 分钟`;
  return `每 ${seconds} 秒`;
}

/** 0.4 秒 / 12 秒 / 3 分 5 秒. */
export function durationText(fromIso: string, toIso: string): string {
  const ms = Math.max(0, new Date(toIso).getTime() - new Date(fromIso).getTime());
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)} 秒`;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s} 秒` : `${Math.floor(s / 60)} 分 ${s % 60} 秒`;
}

export interface JobState {
  label: string;
  type: "success" | "warning" | "danger" | "info" | "primary";
}

/** What the 状态 column shows for a job. */
export function jobState(j: { running: boolean; paused: boolean; run_requested: boolean; last_ok?: boolean | null; last_started_at?: string | null }): JobState {
  if (j.running) return { label: "运行中", type: "primary" };
  if (j.run_requested) return { label: "即将运行", type: "primary" };
  if (j.paused) return { label: "已暂停", type: "info" };
  if (!j.last_started_at) return { label: "还没运行", type: "info" };
  return j.last_ok === false ? { label: "上次失败", type: "danger" } : { label: "正常", type: "success" };
}

export const TRIGGER_LABEL: Record<string, string> = { scheduled: "按间隔", woken: "有新工作", manual: "手动" };
