// Markdown → Vue vnodes with the same pipeline react-markdown uses (remark-parse + remark-gfm →
// remark-rehype → hast → JSX runtime), so web and webv render identical HTML: raw HTML is
// dropped, URLs go through the same safe transform, and bare URLs followed by CJK text are
// fenced first (autolink.ts).
import type { Element, Root } from "hast";
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

const processor = unified().use(remarkParse).use(remarkGfm).use(remarkRehype);

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
export function markdownTree(text: string, transform: UrlTransform = defaultUrlTransform): Root {
  const tree = processor.runSync(processor.parse(fenceCjkUrls(text))) as Root;
  transformUrls(tree, transform);
  return tree;
}

export function renderMarkdown(text: string, transform?: UrlTransform): VNode {
  return toJsxRuntime(markdownTree(text, transform), {
    Fragment,
    jsx: jsx as never,
    jsxs: jsxs as never,
    elementAttributeNameCase: "html",
    stylePropertyNameCase: "css",
    ignoreInvalidStyle: true,
  }) as unknown as VNode;
}
