// Agent Teams desktop: one window on the site (https://agent.prophey.ai or AGENT_TEAMS_URL).
// The page stays same-origin with its API, so cookies, CSRF, SSE and the voice WebSocket work
// as in a browser. Everything else (other sites, permissions, deep links) is decided by the
// pure modules next to this file.
import { BrowserWindow, Menu, app, session, shell, type WebContents } from "electron";
import { join } from "node:path";
import { desktopUserAgent, resolveAppUrl } from "./config";
import { PROTOCOL, deepLinkToUrl, findDeepLink } from "./deeplink";
import { menuTemplate, isDevToolsShortcut } from "./menu";
import { classifyLink } from "./navigation";
import { OFFLINE_PAGE, showsOfflinePage } from "./offline";
import { allowPermission } from "./permissions";
import { checkForUpdatesNow, startAutoUpdate, updatesEnabled } from "./updater";
import { loadWindowState, trackWindowState } from "./windowStore";

const APP_ID = "ai.prophey.agentteams";
const appUrl = resolveAppUrl(process.env);
const appOrigin = appUrl.origin;
const isMac = process.platform === "darwin";
const devMode = !app.isPackaged;

let mainWindow: BrowserWindow | null = null;
let pendingUrl: string | null = null; // a deep link that arrived before the window existed

const webPreferences = (): Electron.WebPreferences => ({
  contextIsolation: true,
  sandbox: true,
  nodeIntegration: false,
  nodeIntegrationInSubFrames: false,
  webviewTag: false,
  spellcheck: false,
  preload: join(__dirname, "preload.js"),
  additionalArguments: [`--agent-teams-version=${app.getVersion()}`],
});

function openExternal(url: string) {
  if (classifyLink(url, appOrigin) === "browser") void shell.openExternal(url);
}

/** Navigation rules for every page the app opens (main window and same-origin popups). */
function guardContents(contents: WebContents) {
  contents.on("will-navigate", (event, url) => {
    if (classifyLink(url, appOrigin) === "app") return;
    event.preventDefault();
    openExternal(url);
  });
  contents.setWindowOpenHandler(({ url }) => {
    if (classifyLink(url, appOrigin) === "app") return { action: "allow", overrideBrowserWindowOptions: childWindowOptions() };
    openExternal(url);
    return { action: "deny" };
  });
  contents.on("will-attach-webview", (event) => event.preventDefault());
  contents.on("did-fail-load", (_e, errorCode, errorDescription, validatedURL, isMainFrame) => {
    if (!showsOfflinePage(errorCode, isMainFrame, validatedURL)) return;
    void contents.loadFile(join(__dirname, OFFLINE_PAGE), { query: { url: validatedURL, error: `${errorDescription} (${errorCode})` } });
  });
  contents.on("before-input-event", (event, input) => {
    if (!isDevToolsShortcut(input, process.platform)) return;
    event.preventDefault();
    contents.toggleDevTools();
  });
}

const childWindowOptions = (): Electron.BrowserWindowConstructorOptions => ({ width: 1100, height: 800, autoHideMenuBar: !isMac, webPreferences: webPreferences() });

function createMainWindow(url: string) {
  const state = loadWindowState();
  const win = new BrowserWindow({
    ...state.bounds,
    minWidth: 480,
    minHeight: 360,
    show: false,
    title: "Agent Teams",
    backgroundColor: "#ffffff",
    autoHideMenuBar: !isMac,
    webPreferences: webPreferences(),
  });
  if (state.maximized) win.maximize();
  trackWindowState(win);
  win.once("ready-to-show", () => win.show());
  win.on("closed", () => (mainWindow = null));
  void win.loadURL(url);
  mainWindow = win;
}

/** Shows the window, opening `url` in it when given (deep links). */
function showApp(url?: string) {
  if (!app.isReady()) {
    pendingUrl = url ?? pendingUrl;
    return;
  }
  if (!mainWindow) return createMainWindow(url ?? appUrl.toString());
  if (url) void mainWindow.loadURL(url);
  if (mainWindow.isMinimized()) mainWindow.restore();
  mainWindow.show();
  mainWindow.focus();
}

const openDeepLink = (link: string | undefined) => {
  const url = link ? deepLinkToUrl(link, appOrigin) : null;
  if (url) showApp(url);
};

function setupPermissions() {
  const ses = session.defaultSession;
  ses.setPermissionRequestHandler((_wc, permission, callback, details) => {
    const mediaTypes = "mediaTypes" in details ? details.mediaTypes : undefined;
    callback(allowPermission({ permission, origin: details.requestingUrl, mediaTypes }, appOrigin));
  });
  ses.setPermissionCheckHandler((_wc, permission, requestingOrigin, details) => {
    const mediaTypes = details.mediaType ? [details.mediaType] : undefined;
    return allowPermission({ permission, origin: requestingOrigin, mediaTypes }, appOrigin);
  });
}

function setupMenu() {
  const template = menuTemplate({
    platform: process.platform,
    appName: app.name,
    devTools: devMode,
    openHome: () => showApp(appUrl.toString()),
    openWebsite: () => void shell.openExternal(mainWindow?.webContents.getURL().startsWith(appOrigin) ? mainWindow.webContents.getURL() : appUrl.toString()),
    checkForUpdates: updatesEnabled(process.env) ? () => void checkForUpdatesNow() : undefined,
  });
  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

function registerProtocol() {
  // In development the app runs as `electron .`, so the path to the app has to be passed along.
  if (process.defaultApp && process.argv[1]) app.setAsDefaultProtocolClient(PROTOCOL, process.execPath, [join(process.argv[1])]);
  else app.setAsDefaultProtocolClient(PROTOCOL);
}

function main() {
  if (!app.requestSingleInstanceLock()) return app.quit();
  app.setAppUserModelId(APP_ID); // Windows: without it notifications do not show
  app.userAgentFallback = desktopUserAgent(app.userAgentFallback, app.getVersion());
  registerProtocol();

  app.on("second-instance", (_e, argv) => {
    const link = findDeepLink(argv);
    if (link) openDeepLink(link);
    else showApp();
  });
  app.on("open-url", (event, link) => {
    event.preventDefault();
    openDeepLink(link);
  });
  app.on("web-contents-created", (_e, contents) => guardContents(contents));
  app.on("activate", () => showApp());
  app.on("window-all-closed", () => {
    if (!isMac) app.quit();
  });

  void app.whenReady().then(() => {
    setupPermissions();
    setupMenu();
    const link = findDeepLink(process.argv);
    const first = pendingUrl ?? (link ? deepLinkToUrl(link, appOrigin) : null);
    createMainWindow(first ?? appUrl.toString());
    if (updatesEnabled(process.env)) startAutoUpdate();
  });
}

main();
