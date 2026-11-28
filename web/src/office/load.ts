// Layout loading (office-layout.md §5, §6): fetch with ETag, resolve
// `extends`, validate against the shared JSON Schema (ajv) and the semantic
// checks, and fall back to the built-in layout on an unknown format.
import { $t } from "@/i18n";
import Ajv2020, { type ErrorObject, type ValidateFunction } from "ajv/dist/2020";
import schemaText from "./office-layout.schema.json?raw";
import { validateSemantics } from "./validate";
import type { OfficeLayout } from "./types";

export const SUPPORTED_FORMAT = 1;

export class LayoutError extends Error {
  constructor(message: string, readonly problems: string[] = []) {
    super(problems.length ? `${message}: ${problems.slice(0, 5).join("; ")}` : message);
  }
}

let validator: ValidateFunction | undefined;

function schemaValidator(): ValidateFunction {
  if (validator) return validator;
  const ajv = new Ajv2020({ allErrors: true, strict: false });
  validator = ajv.compile(JSON.parse(schemaText));
  return validator;
}

function describe(e: ErrorObject): string {
  return `${e.instancePath || "/"} ${e.message ?? "is invalid"}`;
}

/** JSON Schema problems (empty when valid). oneOf noise is reduced to the most specific messages. */
export function schemaProblems(doc: unknown): string[] {
  const validate = schemaValidator();
  if (validate(doc)) return [];
  const errors = validate.errors ?? [];
  const specific = errors.filter((e) => e.keyword !== "oneOf" && e.keyword !== "anyOf");
  return [...new Set((specific.length ? specific : errors).map(describe))];
}

export interface ValidatedLayout {
  layout: OfficeLayout;
  warnings: string[];
}

/** Schema + semantic validation; throws LayoutError with all problems. */
export function validateLayout(doc: unknown): ValidatedLayout {
  const format = (doc as { format?: unknown } | null)?.format;
  if (format !== undefined && format !== SUPPORTED_FORMAT) throw new LayoutError($t("布局格式需要更新的前端 (format {format})", { format: String(format) }));
  const problems = schemaProblems(doc);
  if (problems.length) throw new LayoutError("layout does not match the schema", problems);
  const layout = doc as OfficeLayout;
  const report = validateSemantics(layout);
  if (report.errors.length) throw new LayoutError("layout failed semantic checks", report.errors);
  return { layout, warnings: report.warnings };
}

type Json = Record<string, unknown>;
const BY_ID = new Set(["rooms", "furniture", "widgets"]);

/**
 * `extends` merge: objects deep-merge by key; rooms/furniture/widgets merge by
 * id (same id replaces, new ids append); other arrays are replaced.
 */
export function mergeLayouts(base: Json, child: Json): Json {
  const out: Json = { ...base };
  for (const [k, v] of Object.entries(child)) {
    if (k === "extends") continue;
    out[k] = mergeValue(k, base[k], v);
  }
  return out;
}

function mergeValue(key: string, a: unknown, b: unknown): unknown {
  if (BY_ID.has(key) && Array.isArray(a) && Array.isArray(b)) return mergeById(a as Json[], b as Json[]);
  if (isObject(a) && isObject(b)) return mergeDeep(a, b);
  return b;
}

function mergeDeep(a: Json, b: Json): Json {
  const out: Json = { ...a };
  for (const [k, v] of Object.entries(b)) out[k] = isObject(out[k]) && isObject(v) ? mergeDeep(out[k] as Json, v) : v;
  return out;
}

function mergeById(a: Json[], b: Json[]): Json[] {
  const out = [...a];
  for (const item of b) {
    const i = out.findIndex((x) => x.id === item.id);
    if (i >= 0) out[i] = item;
    else out.push(item);
  }
  return out;
}

function isObject(v: unknown): v is Json {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Resolves the `extends` chain with `fetchDoc(id)` (cycle-safe, depth ≤ 8). */
export async function resolveExtends(doc: Json, fetchDoc: (id: string) => Promise<Json>, depth = 0): Promise<Json> {
  const parent = doc.extends;
  if (typeof parent !== "string") return doc;
  if (depth >= 8) throw new LayoutError("layout extends chain is too deep or cyclic");
  const base = await resolveExtends(await fetchDoc(parent), fetchDoc, depth + 1);
  return mergeLayouts(base, doc);
}

interface CacheEntry {
  etag: string;
  doc: Json;
}

const etagCache = new Map<string, CacheEntry>();

/** GET with If-None-Match; reuses the cached body on 304. */
export async function fetchJson(url: string, fetcher: typeof fetch = fetch): Promise<Json> {
  const cached = etagCache.get(url);
  const res = await fetcher(url, { headers: cached ? { "If-None-Match": cached.etag } : {}, credentials: "same-origin" });
  if (res.status === 304 && cached) return cached.doc;
  if (!res.ok) throw new LayoutError(`GET ${url} failed with ${res.status}`);
  const doc = (await res.json()) as Json;
  const etag = res.headers.get("ETag");
  if (etag) etagCache.set(url, { etag, doc });
  return doc;
}

export function layoutUrl(id?: string): string {
  return id ? `/api/office/layouts/${encodeURIComponent(id)}` : "/api/office/layouts/default";
}

/** Fetches, merges `extends`, validates. */
export async function loadLayout(id?: string, fetcher: typeof fetch = fetch): Promise<ValidatedLayout> {
  const get = (x?: string) => fetchJson(layoutUrl(x), fetcher);
  const doc = await resolveExtends(await get(id), (x) => get(x));
  return validateLayout(doc);
}
