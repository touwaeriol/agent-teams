// The one HTTP client of the web app: openapi-fetch over the generated core
// paths plus the orchestration paths, same-origin cookies, CSRF header on
// writes, and `{detail, code, fields}` errors turned into ApiError.
import { $t, currentLocale, serverText, type ServerText } from "@/i18n";
import createClient, { type Middleware } from "openapi-fetch";
import type { OrchestrationPaths } from "./orchestration";
import type { paths as CorePaths } from "./schema";

export type Paths = CorePaths & OrchestrationPaths;

export const CSRF_COOKIE = "agent_teams_csrf";
export const CSRF_HEADER = "X-CSRF-Token";

export interface FieldProblem {
  path: string;
  message: string;
  text?: ServerText | null;
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
    const b = body as { detail?: unknown; code?: unknown; message?: ServerText | null; fields?: unknown; title?: unknown; errors?: unknown };
    // Field and summary texts in the interface language (the server sends key + params beside its Chinese).
    const fields = (Array.isArray(b.fields) ? (b.fields as FieldProblem[]) : []).map((f) => ({ ...f, message: serverText(f.text ?? { key: f.message }, f.message) }));
    const raw = typeof b.detail === "string" && b.detail ? b.detail : typeof b.title === "string" ? b.title : "";
    const detail = serverText(b.message, raw);
    const head = detail || statusText || $t("请求失败（{status}）", { status });
    // Field problems not already said by the summary (a one-field error repeats it).
    const problems = fields.map((f) => f.message).filter((m) => m && m !== head);
    const cjk = currentLocale() === "zh-CN" || currentLocale() === "ja";
    const message = problems.length ? $t("{detail}：{problems}", { detail: head, problems: problems.join(cjk ? "；" : "; ") }) : head;
    return new ApiError(status, message, typeof b.code === "string" ? b.code : "", fields);
  }
  return new ApiError(status, typeof body === "string" && body ? body : statusText || $t("请求失败（{status}）", { status }));
}

// fetch is looked up per request (not captured at import) so tests can stub it.
export const client = createClient<Paths>({ baseUrl: "", credentials: "same-origin", fetch: (input: Request) => globalThis.fetch(input) });
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
