#!/usr/bin/env node
// Regenerates src/i18n/locales/zh-CN.json from every $t("…") call in src (keys = the Chinese
// text, value = the same text), and reports keys missing from or unused in the other locales.
//   node scripts/i18n-extract.mjs          write zh-CN.json, report
//   node scripts/i18n-extract.mjs --check  fail if zh-CN.json is stale or a locale misses a key
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "../src");
const dir = path.join(root, "i18n/locales");
const check = process.argv.includes("--check");

const files = [];
const walk = (d) => {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name);
    if (e.isDirectory()) {
      if (!["__tests__", "node_modules", "i18n"].includes(e.name)) walk(p);
    } else if (/\.(ts|tsx|vue)$/.test(e.name) && !/\.test\.ts$/.test(e.name)) files.push(p);
  }
};
walk(root);

// $t("…") / $t('…') with a literal first argument (escaped quotes allowed).
const CALL = /\$t\(\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')/g;
const keys = new Set();
for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  for (const m of src.matchAll(CALL)) {
    const lit = m[1];
    const body = lit.slice(1, -1).replace(/\\(.)/g, (_, c) => (c === "n" ? "\n" : c));
    keys.add(body);
  }
}

const sorted = [...keys].sort();
const zh = Object.fromEntries(sorted.map((k) => [k, k]));
const zhPath = path.join(dir, "zh-CN.json");
const text = JSON.stringify(zh, null, 2) + "\n";
let failed = false;
if (check) {
  if (fs.readFileSync(zhPath, "utf8") !== text) {
    console.error("zh-CN.json is stale: run npm run i18n:extract");
    failed = true;
  }
} else fs.writeFileSync(zhPath, text);

for (const l of ["en", "ko", "ja"]) {
  const p = path.join(dir, `${l}.json`);
  const have = fs.existsSync(p) ? JSON.parse(fs.readFileSync(p, "utf8")) : {};
  const missing = sorted.filter((k) => !(k in have));
  const unused = Object.keys(have).filter((k) => !keys.has(k));
  if (missing.length || unused.length) {
    console.log(`${l}: ${missing.length} missing, ${unused.length} unused`);
    if (check && missing.length) failed = true;
  }
}
console.log(`${sorted.length} keys`);
process.exit(failed ? 1 : 0);
