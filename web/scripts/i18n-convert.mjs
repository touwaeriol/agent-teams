#!/usr/bin/env node
// One-off codemod (kept for new code): wraps hard-coded Chinese UI strings in $t("…").
// Keys are the Simplified Chinese source text (gettext style); src/i18n/locales/zh-CN.json is
// regenerated from the $t("…") calls by scripts/i18n-extract.mjs.
//
//   node scripts/i18n-convert.mjs [--dry] <files…>
//
// Handles: .ts/.tsx string and template literals, JSX text, .vue templates (text + simple
// interpolations merged into one message, static attributes → bound, literals inside
// expressions) and <script> blocks. Skips comments, type positions, object keys, comparisons
// (=== / !== / case), strings already inside $t(), console/log calls and messages with
// vue-i18n syntax characters (@ $ | { }) — those are listed for manual handling.
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.resolve(import.meta.dirname, "../package.json"));
const ts = require("typescript");
const { parse: parseSfc } = require("@vue/compiler-sfc");
const { NodeTypes } = require("@vue/compiler-dom");

const HAN = /[一-鿿]/;
const SPECIAL = /[@$|{}]/;
const dry = process.argv.includes("--dry");
const files = process.argv.slice(2).filter((a) => !a.startsWith("--"));
const manual = [];

/** Replacements [start, end, text] applied back to front. */
function applyEdits(src, edits) {
  edits.sort((a, b) => b[0] - a[0]);
  let out = src;
  let last = Infinity;
  for (const [s, e, text] of edits) {
    if (e > last) continue; // overlapping: keep the outer (later-sorted) one
    out = out.slice(0, s) + text + out.slice(e);
    last = s;
  }
  return out;
}

const q = (text, quote) => quote + text.replace(/\\/g, "\\\\").replace(new RegExp(quote, "g"), "\\" + quote).replace(/\n/g, "\\n") + quote;

const GENERIC = new Set(["value", "length", "toLocaleString", "toString", "toFixed", "join", "trim", "min", "max", "round", "floor", "ceil", "abs", "slice", "map", "filter", "String", "Number"]);

function baseName(expr) {
  if (!expr) return "";
  if (ts.isParenthesizedExpression(expr) || ts.isNonNullExpression(expr)) return baseName(expr.expression);
  if (ts.isIdentifier(expr)) return expr.text;
  if (ts.isPropertyAccessExpression(expr)) {
    if (expr.name.text === "length") return (baseName(expr.expression) || "n") + "Count";
    if (expr.name.text === "value") return baseName(expr.expression);
    return expr.name.text;
  }
  if (ts.isCallExpression(expr)) {
    const callee = expr.expression;
    const own = ts.isIdentifier(callee) ? callee.text : ts.isPropertyAccessExpression(callee) ? callee.name.text : "";
    if (ts.isPropertyAccessExpression(callee) && GENERIC.has(own) && !/^(Math|JSON|Object)$/.test(callee.expression.getText())) return baseName(callee.expression);
    if (expr.arguments.length === 1 && !/^(Math)$/.test(ts.isPropertyAccessExpression(callee) ? callee.expression.getText() : "")) {
      const a = baseName(expr.arguments[0]);
      if (a) return a;
    }
    return GENERIC.has(own) ? "" : own;
  }
  return "";
}

function nameFor(expr, used) {
  let base = baseName(expr);
  if (!/^[a-zA-Z_]\w*$/.test(base) || GENERIC.has(base)) base = "n";
  let name = base;
  for (let i = 2; used.has(name); i++) name = base + i;
  used.add(name);
  return name;
}

/** Is this literal in a place where wrapping it changes meaning? */
function skipLiteral(node) {
  const p = node.parent;
  if (!p) return true;
  if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isLiteralTypeNode(p) || ts.isExternalModuleReference(p)) return true;
  if ((ts.isPropertyAssignment(p) || ts.isPropertyDeclaration(p) || ts.isMethodDeclaration(p) || ts.isPropertySignature(p)) && p.name === node) return true;
  if (ts.isElementAccessExpression(p) && p.argumentExpression === node) return true;
  if (ts.isBinaryExpression(p) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken, ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken].includes(p.operatorToken.kind)) return true;
  if (ts.isCaseClause(p)) return true;
  if (ts.isCallExpression(p)) {
    const callee = p.expression.getText();
    if (callee === "$t" || /^console\.|^(it|describe|test|expect)$/.test(callee) || /\.(includes|startsWith|endsWith|indexOf|split|replace|test|match)$/.test(callee)) return true;
  }
  if (ts.isTaggedTemplateExpression(p)) return true;
  return false;
}

/**
 * Edits for every Chinese literal in a TS/TSX source. `quote` is the quote for t("…") (single
 * inside Vue attribute values). `offset` shifts positions (template expressions).
 */
function tsEdits(code, { quote = '"', offset = 0, tsx = false, file = "" } = {}) {
  const sf = ts.createSourceFile("x." + (tsx ? "tsx" : "ts"), code, ts.ScriptTarget.Latest, true, tsx ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const edits = [];
  const visit = (node) => {
    if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && HAN.test(node.text)) {
      if (ts.isJsxAttribute(node.parent)) {
        if (SPECIAL.test(node.text)) manual.push(`${file}: ${node.text}`);
        else edits.push([node.getStart() + offset, node.getEnd() + offset, `{$t(${q(node.text, '"')})}`]);
        return;
      }
      if (skipLiteral(node)) return;
      if (SPECIAL.test(node.text)) {
        manual.push(`${file}: ${node.text}`);
        return;
      }
      edits.push([node.getStart() + offset, node.getEnd() + offset, `$t(${q(node.text, quote)})`]);
      return;
    }
    if (ts.isTemplateExpression(node) && HAN.test(node.getText())) {
      if (skipLiteral(node)) return;
      const parts = [node.head.text, ...node.templateSpans.map((s) => s.literal.text)];
      if (parts.some((p) => SPECIAL.test(p))) {
        manual.push(`${file}: ${node.getText()}`);
        return;
      }
      // Nested Chinese inside ${…} gets converted on its own first (inner edits win below).
      const used = new Set();
      let msg = node.head.text;
      const args = [];
      for (const span of node.templateSpans) {
        const name = nameFor(span.expression, used);
        const inner = applyEdits(span.expression.getText(), tsEdits(span.expression.getText(), { quote, file }).map(([s, e, t]) => [s, e, t]));
        args.push(name === inner ? name : `${name}: ${inner}`);
        msg += `{${name}}` + span.literal.text;
      }
      edits.push([node.getStart() + offset, node.getEnd() + offset, `$t(${q(msg, quote)}, { ${args.join(", ")} })`]);
      return;
    }
    if (ts.isJsxText(node) && HAN.test(node.text)) {
      const text = node.text.trim();
      if (SPECIAL.test(text)) {
        manual.push(`${file}: ${text}`);
        return;
      }
      const lead = node.text.match(/^\s*/)[0];
      const trail = node.text.match(/\s*$/)[0];
      edits.push([node.getStart() + offset, node.getEnd() + offset, `${lead}{$t(${q(text, '"')})}${trail}`]);
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return edits;
}

/** Expression in a Vue template (attribute value or interpolation). */
function exprEdits(code, offset, quote, file) {
  // Wrap so object literals and arrow functions parse as expressions.
  const pre = "(";
  return tsEdits(pre + code + ")", { quote, offset: offset - pre.length, file });
}

function isSimpleExpr(code) {
  const sf = ts.createSourceFile("x.ts", "(" + code + ")", ts.ScriptTarget.Latest, true);
  const e = sf.statements[0]?.expression?.expression;
  return e && (ts.isIdentifier(e) || ts.isPropertyAccessExpression(e) || ts.isCallExpression(e) || ts.isElementAccessExpression(e) || ts.isNonNullExpression(e)) ? e : null;
}

function vueTemplateEdits(src, ast, file) {
  const edits = [];
  const walk = (node) => {
    if (node.type === NodeTypes.ELEMENT) {
      const isPre = node.tag === "pre" || node.tag === "code";
      for (const p of node.props) {
        if (p.type === NodeTypes.ATTRIBUTE && p.value && HAN.test(p.value.content)) {
          const v = p.value.content;
          if (SPECIAL.test(v) || v.includes("'")) {
            manual.push(`${file}: ${p.name}="${v}"`);
            continue;
          }
          edits.push([p.loc.start.offset, p.loc.end.offset, `:${p.name}="$t(${q(v, "'")})"`]);
        } else if (p.type === NodeTypes.DIRECTIVE) {
          if (p.exp && HAN.test(p.exp.loc.source)) edits.push(...exprEdits(p.exp.loc.source, p.exp.loc.start.offset, "'", file));
        }
      }
      if (!isPre) groupChildren(node.children);
      node.children.forEach(walk);
      return;
    }
    if (node.type === NodeTypes.ROOT) {
      groupChildren(node.children);
      node.children.forEach(walk);
    }
  };
  // Runs of text + interpolations under one parent become one message: "已归档 {n}".
  const groupChildren = (children) => {
    let run = [];
    const flush = () => {
      if (run.some((c) => c.type === NodeTypes.TEXT && HAN.test(c.content))) convertRun(run);
      else for (const c of run) if (c.type === NodeTypes.INTERPOLATION && HAN.test(c.content.loc.source)) edits.push(...exprEdits(c.content.loc.source, c.content.loc.start.offset, '"', file));
      run = [];
    };
    for (const c of children) {
      if (c.type === NodeTypes.TEXT || c.type === NodeTypes.INTERPOLATION) run.push(c);
      else if (c.type === NodeTypes.COMMENT) continue;
      else flush();
    }
    flush();
  };
  const convertRun = (run) => {
    // Interpolations that aren't simple (or contain Chinese) split the run.
    const segments = [];
    let cur = [];
    for (const c of run) {
      if (c.type === NodeTypes.INTERPOLATION && (HAN.test(c.content.loc.source) || !isSimpleExpr(c.content.loc.source) || isSimpleExpr(c.content.loc.source).getText().length > 60)) {
        segments.push(cur, [c]);
        cur = [];
      } else cur.push(c);
    }
    segments.push(cur);
    for (const seg of segments) {
      if (!seg.length) continue;
      if (seg.length === 1 && seg[0].type === NodeTypes.INTERPOLATION) {
        const c = seg[0];
        if (HAN.test(c.content.loc.source)) edits.push(...exprEdits(c.content.loc.source, c.content.loc.start.offset, '"', file));
        continue;
      }
      if (!seg.some((c) => c.type === NodeTypes.TEXT && HAN.test(c.content))) continue;
      const raw = src.slice(seg[0].loc.start.offset, seg[seg.length - 1].loc.end.offset);
      const lead = raw.match(/^\s*/)[0];
      const trail = raw.match(/\s*$/)[0];
      const used = new Set();
      let msg = "";
      const args = [];
      for (const c of seg) {
        if (c.type === NodeTypes.TEXT) msg += src.slice(c.loc.start.offset, c.loc.end.offset);
        else {
          const e = isSimpleExpr(c.content.loc.source);
          const name = nameFor(e, used);
          args.push(name === e.getText() ? name : `${name}: ${c.content.loc.source.trim()}`);
          msg += `{${name}}`;
        }
      }
      msg = msg.trim().replace(/\s*\n\s*/g, " ");
      const literal = seg.filter((c) => c.type === NodeTypes.TEXT).map((c) => c.content).join("");
      if (SPECIAL.test(literal) || /&\w+;/.test(literal)) {
        manual.push(`${file}: ${msg}`);
        continue;
      }
      const call = args.length ? `$t(${q(msg, '"')}, { ${args.join(", ")} })` : `$t(${q(msg, '"')})`;
      edits.push([seg[0].loc.start.offset, seg[seg.length - 1].loc.end.offset, `${lead}{{ ${call} }}${trail}`]);
    }
  };
  walk(ast);
  return edits;
}

const IMPORT = 'import { $t } from "@/i18n";\n';

function addImport(code) {
  if (/import \{[^}]*\$t\b[^}]*\} from "@\/i18n"/.test(code)) return code;
  // After the leading comment block.
  const m = code.match(/^(?:\s*\/\/[^\n]*\n|\s*\/\*[\s\S]*?\*\/\n)*/);
  return code.slice(0, m[0].length) + IMPORT + code.slice(m[0].length);
}

function convertTs(file, src) {
  const edits = tsEdits(src, { tsx: file.endsWith(".tsx"), file });
  if (!edits.length) return src;
  return addImport(applyEdits(src, edits));
}

function convertVue(file, src) {
  const { descriptor, errors } = parseSfc(src, { filename: file });
  if (errors.length) throw new Error(`${file}: ${errors[0].message}`);
  const edits = [];
  if (descriptor.template) edits.push(...vueTemplateEdits(src, descriptor.template.ast, file));
  const script = descriptor.scriptSetup ?? descriptor.script;
  for (const block of [descriptor.script, descriptor.scriptSetup].filter(Boolean)) {
    edits.push(...tsEdits(block.content, { offset: block.loc.start.offset, file, tsx: block.lang === "tsx" }));
  }
  if (!edits.length) return src;
  let out = applyEdits(src, edits);
  if (!/import \{[^}]*\$t\b[^}]*\} from "@\/i18n"/.test(out)) {
    if (!script) out = `<script setup lang="ts">\n${IMPORT}</script>\n\n` + out;
    else {
      const re = /<script\b[^>]*\bsetup\b[^>]*>\n/;
      const m = out.match(re) ?? out.match(/<script\b[^>]*>\n/);
      const at = m.index + m[0].length;
      const rest = out.slice(at);
      const lead = rest.match(/^(?:\s*\/\/[^\n]*\n)*/)[0];
      out = out.slice(0, at) + lead + IMPORT + rest.slice(lead.length);
    }
  }
  return out;
}

let changed = 0;
for (const file of files) {
  const src = fs.readFileSync(file, "utf8");
  const out = file.endsWith(".vue") ? convertVue(file, src) : convertTs(file, src);
  if (out !== src) {
    changed++;
    if (!dry) fs.writeFileSync(file, out);
  }
}
console.log(`${changed} file(s) ${dry ? "would change" : "changed"}`);
if (manual.length) console.log("Manual:\n  " + manual.join("\n  "));
