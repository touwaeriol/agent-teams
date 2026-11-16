/**
 * Links that come from employees or users (deliverables, check runs, PRs) are only followed when
 * they are absolute http(s) URLs: a javascript:/data: link would run inside the app when clicked.
 */
export function safeHref(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined;
  try {
    const u = new URL(raw.trim());
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}

/** URL safe to embed in an iframe: http(s) and not this app's own origin (sandbox allows scripts + same-origin). */
export function embeddableUrl(raw: string | null | undefined, appOrigin: string): string | undefined {
  const href = safeHref(raw);
  if (!href) return undefined;
  return new URL(href).origin === appOrigin ? undefined : href;
}
