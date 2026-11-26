// The local page shown when the site does not load (offline, DNS, TLS…).

export const OFFLINE_PAGE = "offline.html";

/** Load failures worth the offline page: main frame only, and not -3 (ERR_ABORTED, a navigation replaced by another). */
export const showsOfflinePage = (errorCode: number, isMainFrame: boolean) => isMainFrame && errorCode !== -3 && errorCode !== 0;
