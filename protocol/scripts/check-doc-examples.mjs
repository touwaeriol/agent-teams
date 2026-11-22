// Checks every message example in docs/v2/protocol.md against the schemas.
//
//   * fenced ```json blocks: comments and "…" placeholders are normalised, "a|b|c" strings are expanded into one
//     variant per alternative, and each variant must validate (formats off: "…" stands for real ids and times);
//   * inline code spans such as `session.delivered {session_id, msg_id, turn_id?}` or `{"job_id", "cwd"}`:
//     every field must exist on that message body, and literal values (`state:"parked"`) must be valid for it;
//   * the session.event kind table (4.8) and the node management table (9): kinds and fields must exist.
// Section 11 (protocol 1 compatibility) is skipped: dropped by the one-shot cut-over (architecture.md 14).
// Usage: node scripts/check-doc-examples.mjs [path/to/protocol.md]
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { ROOT, loadSchema, messageSchemas, schemaId } from "./lib.mjs";
import { createAjv, describeErrors } from "./validator.mjs";

const DOC = process.argv[2] ?? join(ROOT, "../docs/v2/protocol.md");
const ajv = createAjv({ formats: false });
const TYPES = new Set(messageSchemas().map((m) => m.type));
const PLACEHOLDER_VALUES = new Set(["bool", "n", "N", "…", "...", "[…]", "[...]"]);
const SKIP_SECTION = /^## 11\./;

const results = { ok: 0, failed: [] };

function fail(where, msg) {
  results.failed.push(`${where}: ${msg}`);
}

function pass() {
  results.ok += 1;
}

// ---- markdown walking ----------------------------------------------------------------------

function headingType(line) {
  const names = [...line.matchAll(/`([a-z_.]+)`/g)].map((m) => m[1]).filter((n) => TYPES.has(n));
  return names.at(-1) ?? null;
}

function* sections(lines) {
  let type = null;
  let skip = false;
  let fence = null;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (fence) {
      if (line.trim().startsWith("```")) {
        if (!skip && fence.lang === "json") yield { kind: "block", type, text: fence.body.join("\n"), line: fence.line };
        fence = null;
      } else fence.body.push(line);
      continue;
    }
    if (line.trim().startsWith("```")) {
      fence = { lang: line.trim().slice(3).trim(), body: [], line: i + 1 };
      continue;
    }
    if (/^#{2,4} /.test(line)) {
      if (/^## /.test(line)) skip = SKIP_SECTION.test(line);
      type = headingType(line);
      continue;
    }
    if (!skip) yield { kind: "line", type, text: line, line: i + 1 };
  }
}

// ---- fenced JSON blocks ---------------------------------------------------------------------

function stripComments(text) {
  let out = "";
  let inString = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      out += c;
      if (c === "\\") out += text[++i];
      else if (c === '"') inString = false;
    } else if (c === '"') {
      inString = true;
      out += c;
    } else if (c === "/" && text[i + 1] === "/") {
      while (i < text.length && text[i] !== "\n") i++;
      out += "\n";
    } else out += c;
  }
  return out;
}

function normalise(text) {
  return stripComments(text)
    .replace(/\{\s*…\s*\}/g, "{}")
    .replace(/\[\s*…\s*\]/g, "[]")
    .replace(/,\s*…\s*(?=[\]}])/g, "");
}

function pipeStrings(node, acc = []) {
  if (typeof node === "string" && node.includes("|")) acc.push(node.split("|").length);
  else if (node && typeof node === "object") Object.values(node).forEach((v) => pipeStrings(v, acc));
  return acc;
}

function pick(node, i) {
  if (typeof node === "string" && node.includes("|")) {
    const alts = node.split("|");
    return alts[i % alts.length];
  }
  if (Array.isArray(node)) return node.map((v) => pick(v, i));
  if (node && typeof node === "object") return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, pick(v, i)]));
  return node;
}

function variants(doc) {
  const n = Math.max(1, ...pipeStrings(doc));
  return Array.from({ length: n }, (_, i) => pick(doc, i));
}

function blockTarget(doc, type) {
  const placeholderBody = doc.body && Object.keys(doc.body).length === 0;
  if ("v" in doc && "body" in doc) return placeholderBody ? ["envelope", schemaId("node/envelope.json"), doc] : ["frame", schemaId("node/message.json"), doc];
  if ("type" in doc && "body" in doc) return [doc.type, schemaId(`node/${doc.type}.json`), doc.body];
  return type ? [type, schemaId(`node/${type}.json`), doc] : [null, null, doc];
}

function checkBlock({ text, type, line }) {
  const where = `protocol.md:${line}`;
  let doc;
  try {
    doc = JSON.parse(normalise(text));
  } catch (err) {
    return fail(where, `json block does not parse after normalisation: ${err.message}`);
  }
  const [label, id, value] = blockTarget(doc, type);
  if (!id) return fail(where, "json block outside any message section");
  const validate = ajv.getSchema(id);
  for (const v of variants(value)) {
    if (!validate(v)) return fail(where, `${label}: ${describeErrors(validate)}`);
  }
  pass();
}

// ---- inline shorthand spans -----------------------------------------------------------------

function splitTopLevel(inner) {
  const parts = [];
  let depth = 0;
  let cur = "";
  let quote = null;
  for (const c of inner) {
    if (quote) {
      cur += c;
      if (c === quote) quote = null;
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    if ("{[(".includes(c)) depth++;
    if ("}])".includes(c)) depth--;
    if (c === "," && depth === 0) {
      parts.push(cur.trim());
      cur = "";
    } else cur += c;
  }
  if (cur.trim()) parts.push(cur.trim());
  return parts;
}

function parseField(part) {
  const m = part.match(/^["']?([A-Za-z_][A-Za-z0-9_]*)["']?(\?)?(\[\])?\s*(?::\s*(.*))?$/s);
  if (!m) return null;
  return { key: m[1], value: m[4]?.trim() };
}

function literal(value) {
  if (value === undefined || PLACEHOLDER_VALUES.has(value) || /^[[{]/.test(value)) return undefined;
  if (/^["'].*["']$/.test(value)) return value.slice(1, -1);
  if (value === "true" || value === "false") return value === "true";
  if (/^-?\d+(\.\d+)?$/.test(value)) return Number(value);
  if (/^[a-z_]+$/.test(value)) return value;
  return undefined;
}

function fieldAlternatives(part) {
  const whole = parseField(part);
  if (whole) return [whole];
  return part.split("|").map((alt) => parseField(alt.trim()));
}

function checkValue(type, field, where) {
  const raw = literal(field.value);
  if (raw === undefined) return true;
  const validate = ajv.compile({ $ref: `${schemaId(`node/${type}.json`)}#/properties/${field.key}` });
  const values = typeof raw === "string" ? raw.split("|") : [raw];
  const bad = values.find((v) => !validate(v));
  if (bad === undefined) return true;
  fail(where, `${type}.${field.key}=${JSON.stringify(bad)}: ${describeErrors(validate)}`);
  return false;
}

function checkFields(type, inner, where) {
  const props = loadSchema(`node/${type}.json`).properties ?? {};
  for (const part of splitTopLevel(inner)) {
    if (part === "…" || part === "...") continue;
    for (const field of fieldAlternatives(part)) {
      if (!field) return fail(where, `${type}: cannot read field "${part}"`);
      if (!(field.key in props)) return fail(where, `${type}: unknown field ${field.key}`);
      if (!checkValue(type, field, where)) return;
    }
  }
  pass();
}

function explicitType(span) {
  const typed = span.match(/^([a-z_]+(?:\.[a-z_]+)?)\s*\{(.*)\}$/s);
  if (typed && TYPES.has(typed[1])) return [typed[1], typed[2]];
  const envelope = span.match(/^\{\s*type:\s*"([a-z_.]+)",\s*body:\s*\{(.*)\}\s*\}$/s);
  return envelope ? [envelope[1], envelope[2]] : null;
}

function isJson(span) {
  try {
    JSON.parse(normalise(span));
    return true;
  } catch {
    return false;
  }
}

function checkSpan(span, type, line) {
  const where = `protocol.md:${line}`;
  const explicit = explicitType(span);
  if (explicit) return checkFields(explicit[0], explicit[1], where);
  if (!/^\{.*\}$/s.test(span)) return;
  if (isJson(span)) return checkBlock({ text: span, type, line });
  if (!type) return fail(where, `shorthand ${span} outside a message section`);
  checkFields(type, span.slice(1, -1), where);
}

function checkInline({ text, type, line }) {
  if (text.trimStart().startsWith("|")) return;
  for (const m of text.matchAll(/`([^`]+)`/g)) checkSpan(m[1].trim(), type, line);
}

// ---- tables ---------------------------------------------------------------------------------

function cells(line) {
  return line.split("|").slice(1, -1).map((c) => c.trim());
}

function codeNames(cell) {
  return [...cell.matchAll(/`([^`]+)`/g)].map((m) => m[1].replace(/\[\]$|\?$|\{\}$|:.*$/g, "").trim());
}

function checkEventTable(lines) {
  const props = loadSchema("node/session.event.json").properties;
  const start = lines.findIndex((l) => l.startsWith("| `kind` |"));
  for (let i = start + 2; lines[i]?.startsWith("|"); i++) {
    const [kindCell, fieldsCell] = cells(lines[i]);
    const kind = codeNames(kindCell)[0];
    const where = `protocol.md:${i + 1}`;
    if (!props.kind.enum.includes(kind)) fail(where, `session.event kind ${kind} not in schema`);
    const unknown = codeNames(fieldsCell).filter((f) => !/^[a-z_]+\|/.test(f)).filter((f) => !(f in props));
    if (unknown.length) fail(where, `session.event fields not in schema: ${unknown.join(", ")}`);
    else pass();
  }
}

function checkNodeTable(lines) {
  const start = lines.findIndex((l) => l.startsWith("| 消息 | 方向 | 字段 |"));
  for (let i = start + 2; lines[i]?.startsWith("|"); i++) {
    const [typeCell, , fieldsCell] = cells(lines[i]);
    const type = codeNames(typeCell)[0];
    const where = `protocol.md:${i + 1}`;
    if (!TYPES.has(type)) {
      fail(where, `table names unknown message ${type}`);
      continue;
    }
    const props = loadSchema(`node/${type}.json`).properties;
    const unknown = codeNames(fieldsCell).filter((f) => !(f in props));
    if (unknown.length) fail(where, `${type} fields not in schema: ${unknown.join(", ")}`);
    else pass();
  }
}

// ---- main -----------------------------------------------------------------------------------

function run() {
  const lines = readFileSync(DOC, "utf8").split("\n");
  for (const item of sections(lines)) {
    if (item.kind === "block") checkBlock(item);
    else checkInline(item);
  }
  checkEventTable(lines);
  checkNodeTable(lines);
  for (const f of results.failed) console.error("FAIL " + f);
  console.log(`protocol.md examples: ${results.ok} passed, ${results.failed.length} failed`);
  if (results.failed.length) process.exit(1);
}

run();
