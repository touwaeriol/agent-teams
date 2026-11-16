// Helpers for hosted deliverables (files published with publish_file), pure and unit-tested.
import type { DeliverableView } from "@/api/views";

/** Published with publish_file: the console hosts its files and the viewer renders them. */
export function isHosted(d: Pick<DeliverableView, "detail">): boolean {
  return d.detail?.hosted === true;
}

/** A hosted page or image: lists show a picture of it rather than its type icon. */
export function hasPicture(d: Pick<DeliverableView, "detail">): boolean {
  return isHosted(d) && (d.detail.render === "html" || d.detail.render === "image");
}

/**
 * iframe sandbox for a published page. Without a separate file origin the page must never get
 * allow-same-origin: it would run as the console and could use the session (the server's CSP
 * sandbox says the same). With AGENT_TEAMS_ARTIFACT_ORIGIN it may keep its own origin.
 */
export function frameSandbox(isolated: boolean): string {
  const base = "allow-scripts allow-popups allow-forms allow-modals allow-downloads";
  return isolated ? `${base} allow-same-origin` : base;
}

/** A file inside a signed version directory; each path segment is escaped. */
export function fileUrl(baseUrl: string, path: string): string {
  return baseUrl + path.split("/").map(encodeURIComponent).join("/");
}

const PLACEHOLDER = "http://placeholder.invalid/";

/**
 * URL rewriting for Markdown documents: relative links and images resolve inside the
 * published version, absolute http(s) stay, everything else (javascript:, data:) is dropped.
 */
export function markdownUrl(baseUrl: string): (url: string) => string {
  return (url) => {
    if (/^https?:\/\//i.test(url) || url.startsWith("#")) return url;
    if (/^[a-z][a-z0-9+.-]*:/i.test(url) || url.startsWith("/") || url.startsWith("//")) return "";
    try {
      const root = new URL(baseUrl, PLACEHOLDER);
      const abs = new URL(url, root);
      if (!abs.href.startsWith(root.href)) return ""; // ../ out of the published version
      return root.origin === new URL(PLACEHOLDER).origin ? abs.href.slice(root.origin.length) : abs.href;
    } catch {
      return "";
    }
  };
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const RENDER_LABEL: Record<string, string> = { html: "网页", markdown: "Markdown 文档", image: "图片", pdf: "PDF", text: "文本" };

export function renderLabel(render: string | undefined): string {
  return RENDER_LABEL[render ?? ""] ?? "文件";
}
