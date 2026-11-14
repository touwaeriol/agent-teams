// Thin client for the agent-teams HTTP API (cookie session, same origin).

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

type Json = Record<string, unknown> | unknown[];

export async function api<T = unknown>(path: string, options: { method?: string; json?: Json } = {}): Promise<T> {
  const init: RequestInit = { method: options.method ?? "GET", credentials: "same-origin", headers: {} };
  if (options.json !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(options.json);
  }
  const response = await fetch(path, init);
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  const data = text ? safeParse(text) : undefined;
  if (!response.ok) throw new ApiError(response.status, describe(data, response.statusText));
  return data as T;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

function describe(data: unknown, fallback: string): string {
  if (data && typeof data === "object" && "detail" in data) {
    const detail = (data as { detail: unknown }).detail;
    if (typeof detail === "string") return detail;
    if (Array.isArray(detail)) return detail.map((d) => (d as { msg?: string }).msg ?? String(d)).join("；");
  }
  return typeof data === "string" && data ? data : fallback;
}

export interface User {
  id: string;
  email: string;
  role: "admin" | "user";
  active: boolean;
  max_nodes: number | null;
  created_at: string;
}

export interface Project {
  id: string;
  name: string;
  description: string;
  context: string;
  repo: string | null;
  branch: string;
  git_connection_id: string | null;
  created_at: string;
  updated_at: string;
}

export interface GitConnection {
  id: string;
  name: string;
  provider: "github";
  login: string;
  created_at: string;
}

export interface Repo {
  full_name: string;
  private: boolean;
  default_branch: string;
  description: string;
  html_url: string;
  pushed_at: string | null;
}

export interface Conversation {
  id: string;
  title: string;
  created_at: string;
  project_id: string | null;
}

export interface Message {
  id: number;
  role: "user" | "leader" | "error";
  content: string;
  created_at: string;
}

export type TaskStatus = "pending" | "running" | "done" | "failed" | "cancelled";

export interface Task {
  id: string;
  assignee: string;
  instruction: string;
  depends_on: string[];
  status: TaskStatus;
  output: string;
}

export interface NodeInfo {
  id: string;
  name: string;
  created_at: string;
  online: boolean;
  host?: string;
  root?: string;
  allow?: string[];
  max_runs?: number;
  active?: string[];
  claude?: string;
  since?: number;
}

export interface Enrollment {
  token: string;
  install: string;
}

export interface Employee {
  name: string;
  type?: string;
  node?: string;
  description?: string;
  instructions?: string;
  model?: string;
  permission_mode?: string;
  allowed_tools?: string[];
  disallowed_tools?: string[];
  max_turns?: number;
  [key: string]: unknown;
}

export interface TeamDefinition {
  leader?: string;
  employees?: Employee[];
  [key: string]: unknown;
}

export interface TeamState {
  config: string;
  team: TeamDefinition | null;
  saved: boolean;
  error: string;
}

export interface PlatformSettings {
  registration_open: boolean;
  default_max_nodes: number;
}

export interface AdminUser extends User {
  nodes: number;
}
