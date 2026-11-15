// The one HTTP client of the web app: openapi-fetch over the generated core
// paths plus the orchestration paths, same-origin cookies, CSRF header on
// writes, and `{detail, code, fields}` errors turned into ApiError.
import createClient, { type Middleware } from "openapi-fetch";
import type { OrchestrationPaths } from "./orchestration";
import type { paths as CorePaths } from "./schema";

export type Paths = CorePaths & OrchestrationPaths;

export const CSRF_COOKIE = "agent_teams_csrf";
export const CSRF_HEADER = "X-CSRF-Token";

export interface FieldProblem {
  path: string;
  message: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public code = "",
    public fields: FieldProblem[] = [],
  ) {
    super(message);
  }
}

/** Reads one cookie from a `document.cookie` string. */
export function readCookie(cookies: string, name: string): string {
  for (const part of cookies.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return decodeURIComponent(v.join("="));
  }
  return "";
}

export function isWrite(method: string): boolean {
  return !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
}

export function csrfToken(): string {
  return typeof document === "undefined" ? "" : readCookie(document.cookie, CSRF_COOKIE);
}

const csrf: Middleware = {
  onRequest({ request }) {
    const token = csrfToken();
    if (isWrite(request.method) && token) request.headers.set(CSRF_HEADER, token);
    return request;
  },
};

/** Turns an error body (api.md §0) into a message; falls back to the status text. */
export function toApiError(status: number, body: unknown, statusText = ""): ApiError {
  if (body && typeof body === "object") {
    const b = body as { detail?: unknown; code?: unknown; fields?: unknown; title?: unknown; errors?: unknown };
    const fields = Array.isArray(b.fields) ? (b.fields as FieldProblem[]) : [];
    const detail = typeof b.detail === "string" && b.detail ? b.detail : typeof b.title === "string" ? b.title : "";
    const extra = fields.length ? `：${fields.map((f) => f.message).join("；")}` : "";
    return new ApiError(status, (detail || statusText || `请求失败（${status}）`) + extra, typeof b.code === "string" ? b.code : "", fields);
  }
  return new ApiError(status, typeof body === "string" && body ? body : statusText || `请求失败（${status}）`);
}

export const client = createClient<Paths>({ baseUrl: "", credentials: "same-origin" });
client.use(csrf);

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Awaits an openapi-fetch call and returns its data or throws ApiError. */
export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await call;
  if (!response.ok || error !== undefined) throw toApiError(response.status, error, response.statusText);
  return data as T;
}

/** For arrays the core marks nullable (`T[] | null`). */
export async function list<T>(call: Promise<FetchResult<T[] | null>>): Promise<T[]> {
  return (await unwrap(call)) ?? [];
}
