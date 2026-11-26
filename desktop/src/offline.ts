// The local page shown when the site does not load (offline, DNS, TLS…).

export const OFFLINE_PAGE = "offline.html";

/**
 * Load failures worth the offline page: the main frame loading an http(s) page, except -3
 * (ERR_ABORTED, a navigation replaced by another). Never for the offline page itself, which
 * would otherwise reload itself forever if it failed.
 */
export const showsOfflinePage = (errorCode: number, isMainFrame: boolean, url: string) =>
  isMainFrame && errorCode !== -3 && errorCode !== 0 && /^https?:/i.test(url);
