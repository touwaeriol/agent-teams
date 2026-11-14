// Shared helpers for the protocol scripts: locate, load and describe schema files.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

export const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
export const SCHEMAS = join(ROOT, "schemas");
export const ID_BASE = "https://agent-teams.dev/schemas/";
export const DRAFT = "https://json-schema.org/draft/2020-12/schema";
export const GENERATED_MESSAGE = "node/message.json";

export function readJson(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

export function writeJsonText(value) {
  return JSON.stringify(value, null, 2) + "\n";
}

/** Relative paths (e.g. "node/hello.json") of every schema file, sorted. */
export function schemaFiles(group) {
  const dir = join(SCHEMAS, group);
  return readdirSync(dir)
    .filter((name) => name.endsWith(".json"))
    .sort()
    .map((name) => relative(SCHEMAS, join(dir, name)));
}

export function loadSchema(rel) {
  return readJson(join(SCHEMAS, rel));
}

export function schemaId(rel) {
  return ID_BASE + rel;
}

/** Message type for a node message file ("node/session.open.json" -> "session.open"); null for helper files. */
export function messageType(rel, schema) {
  if (!rel.startsWith("node/") || !schema["x-direction"]) return null;
  return rel.slice("node/".length, -".json".length);
}

/** Node message files: [{rel, type, schema}] for every file that defines a message body. */
export function messageSchemas() {
  return schemaFiles("node")
    .map((rel) => ({ rel, schema: loadSchema(rel) }))
    .map(({ rel, schema }) => ({ rel, schema, type: messageType(rel, schema) }))
    .filter((m) => m.type !== null);
}

/** Platform tool files: [{rel, name, schema}] for every file with x-tool metadata. */
export function toolSchemas() {
  return schemaFiles("tools")
    .map((rel) => ({ rel, schema: loadSchema(rel) }))
    .filter(({ schema }) => schema["x-tool"])
    .map(({ rel, schema }) => ({ rel, schema, name: rel.slice("tools/".length, -".json".length) }));
}

export function pascal(type) {
  return type
    .split(/[._]/)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join("");
}
