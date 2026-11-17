// Markdown → Vue vnodes with the same pipeline react-markdown uses (remark-parse + remark-gfm →
// remark-rehype → hast → JSX runtime), so it renders the same HTML the React app did: raw HTML is
// shown as text (react-markdown without skipHtml), URLs go through the same safe transform, and bare URLs followed by CJK text are
// fenced first (autolink.ts).
import type { Element, Root, RootContent } from "hast";
import { toJsxRuntime } from "hast-util-to-jsx-runtime";
import { urlAttributes } from "html-url-attributes";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";
import type { VNode } from "vue";
import { Fragment, jsx, jsxs } from "vue/jsx-runtime";
import { fenceCjkUrls } from "./autolink";

export type UrlTransform = (url: string) => string;

const SAFE_PROTOCOL = /^(https?|ircs?|mailto|xmpp)$/i;

/** react-markdown's defaultUrlTransform: relative URLs and safe protocols pass, anything else becomes "". */
export function defaultUrlTransform(value: string): string {
  const colon = value.indexOf(":");
  const question = value.indexOf("?");
  const hash = value.indexOf("#");
  const slash = value.indexOf("/");
  const relative = colon === -1 || (slash !== -1 && colon > slash) || (question !== -1 && colon > question) || (hash !== -1 && colon > hash);
  return relative || SAFE_PROTOCOL.test(value.slice(0, colon)) ? value : "";
}

type MdNode = { type: string; value?: string; children?: MdNode[] };

/**
 * remark-breaks without the dependency: every line ending inside text becomes a hard break, so a
 * brief written line by line ("目标：…\n约束：…") keeps its lines. Text nodes only occur in phrasing
 * content (never in code), so nothing else changes.
 */
export function remarkBreaks() {
  const walk = (node: MdNode): void => {
    if (!node.children) return;
    node.children = node.children.flatMap((child): MdNode[] => {
      if (child.type !== "text" || !child.value?.includes("\n")) {
        walk(child);
        return [child];
      }
      return child.value.split(/\r?\n|\r/).flatMap((part, i): MdNode[] => [...(i > 0 ? [{ type: "break" }] : []), ...(part ? [{ type: "text", value: part }] : [])]);
    });
  };
  return (tree: MdNode) => walk(tree);
}

const processor = unified().use(remarkParse).use(remarkGfm).use(remarkRehype, { allowDangerousHtml: true });
const breaksProcessor = unified().use(remarkParse).use(remarkGfm).use(remarkBreaks).use(remarkRehype, { allowDangerousHtml: true });

export interface MarkdownOptions {
  /** Links as text, no images (see stripForPreview). */
  preview?: boolean;
  /** Single line endings are hard breaks (a thread's brief), not soft wraps. */
  breaks?: boolean;
}

/** react-markdown: raw HTML nodes become plain text (never markup). */
function rawAsText(node: Root | Element): void {
  node.children = node.children.map((child) => (child.type === "raw" ? ({ type: "text", value: child.value } as RootContent) : child)) as typeof node.children;
  for (const child of node.children) if (child.type === "element") rawAsText(child);
}

function transformUrls(node: Root | Element, transform: UrlTransform): void {
  for (const child of node.children) {
    if (child.type !== "element") continue;
    for (const key of Object.keys(urlAttributes)) {
      const tags = urlAttributes[key];
      const value = child.properties[key];
      if (typeof value === "string" && (tags === null || tags.includes(child.tagName))) child.properties[key] = transform(value);
    }
    transformUrls(child, transform);
  }
}

/** The hast tree of a Markdown text (exported for tests). */
export function markdownTree(text: string, transform: UrlTransform = defaultUrlTransform, opts: Pick<MarkdownOptions, "breaks"> = {}): Root {
  const p = opts.breaks ? breaksProcessor : processor;
  const tree = p.runSync(p.parse(fenceCjkUrls(text))) as Root;
  rawAsText(tree);
  transformUrls(tree, transform);
  return tree;
}

/** Wide tables scroll sideways in their own box instead of widening the chat. */
export function wrapTables(node: Root | Element): void {
  node.children = node.children.map((child) => {
    if (child.type !== "element") return child;
    if (child.tagName === "table") return { type: "element", tagName: "div", properties: { className: ["at-table-scroll"] }, children: [child] } as Element;
    wrapTables(child);
    return child;
  }) as typeof node.children;
}

/**
 * For small previews inside a link (cards): links become plain text (no nested <a>, nothing to
 * focus) and images are dropped from the tree entirely, so the browser never downloads them.
 */
export function stripForPreview(node: Root | Element): void {
  const out: RootContent[] = [];
  for (const child of node.children as RootContent[]) {
    if (child.type !== "element") out.push(child);
    else if (child.tagName !== "img") {
      stripForPreview(child);
      out.push(child.tagName === "a" ? ({ type: "element", tagName: "span", properties: {}, children: child.children } as Element) : child);
    }
  }
  node.children = out as typeof node.children;
}

export function renderMarkdown(text: string, transform?: UrlTransform, opts: MarkdownOptions = {}): VNode {
  const tree = markdownTree(text, transform, opts);
  if (opts.preview) stripForPreview(tree);
  wrapTables(tree);
  return toJsxRuntime(tree, {
    Fragment,
    jsx: jsx as never,
    jsxs: jsxs as never,
    elementAttributeNameCase: "html",
    stylePropertyNameCase: "css",
    ignoreInvalidStyle: true,
  }) as unknown as VNode;
}
