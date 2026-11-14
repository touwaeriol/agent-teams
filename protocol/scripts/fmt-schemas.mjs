// Normalises schema source files: adds $schema/$id, orders the header keys, pretty-prints.
// Usage: node scripts/fmt-schemas.mjs [--check]
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { DRAFT, SCHEMAS, schemaFiles, schemaId, writeJsonText } from "./lib.mjs";

const HEADER = ["$schema", "$id", "title", "description"];

function normalise(rel, schema) {
  const head = { $schema: DRAFT, $id: schemaId(rel), title: schema.title, description: schema.description };
  const meta = Object.keys(schema).filter((k) => k.startsWith("x-")).sort();
  const rest = Object.keys(schema).filter((k) => !HEADER.includes(k) && !k.startsWith("x-"));
  const out = {};
  for (const key of HEADER) if (head[key] !== undefined) out[key] = head[key];
  for (const key of [...meta, ...rest]) out[key] = schema[key];
  return out;
}

function run(check) {
  const stale = [];
  for (const rel of [...schemaFiles("node"), ...schemaFiles("tools")]) {
    const path = join(SCHEMAS, rel);
    const before = readFileSync(path, "utf8");
    const after = writeJsonText(normalise(rel, JSON.parse(before)));
    if (before === after) continue;
    stale.push(rel);
    if (!check) writeFileSync(path, after);
  }
  if (check && stale.length) {
    console.error("not formatted:\n  " + stale.join("\n  "));
    process.exit(1);
  }
}

run(process.argv.includes("--check"));
