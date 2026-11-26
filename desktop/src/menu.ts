// The application menu. Pure (builds the template; main.ts turns it into a Menu).
// Mac needs the Edit menu for Cmd+C / Cmd+V to work at all.
import type { MenuItemConstructorOptions } from "electron";

export interface MenuOptions {
  platform: NodeJS.Platform;
  appName: string;
  /** Show 开发者工具 in the View menu (development builds). Packaged builds use the shortcut. */
  devTools: boolean;
  openHome: () => void;
  openWebsite: () => void;
  checkForUpdates?: () => void;
}

const sep: MenuItemConstructorOptions = { type: "separator" };

function appMenu(o: MenuOptions): MenuItemConstructorOptions {
  return {
    label: o.appName,
    submenu: [
      { role: "about", label: `关于 ${o.appName}` },
      ...(o.checkForUpdates ? [{ label: "检查更新…", click: o.checkForUpdates }] : []),
      sep,
      { role: "services", label: "服务" },
      sep,
      { role: "hide", label: `隐藏 ${o.appName}` },
      { role: "hideOthers", label: "隐藏其他" },
      { role: "unhide", label: "全部显示" },
      sep,
      { role: "quit", label: `退出 ${o.appName}` },
    ],
  };
}

const fileMenu = (o: MenuOptions): MenuItemConstructorOptions => ({
  label: "文件",
  submenu: [{ label: "回到首页", accelerator: "CmdOrCtrl+Shift+H", click: o.openHome }, sep, o.platform === "darwin" ? { role: "close", label: "关闭窗口" } : { role: "quit", label: "退出" }],
});

const editMenu = (o: MenuOptions): MenuItemConstructorOptions => ({
  label: "编辑",
  submenu: [
    { role: "undo", label: "撤销" },
    { role: "redo", label: "重做" },
    sep,
    { role: "cut", label: "剪切" },
    { role: "copy", label: "复制" },
    { role: "paste", label: "粘贴" },
    ...(o.platform === "darwin" ? [{ role: "pasteAndMatchStyle", label: "粘贴并匹配样式" } as const] : []),
    { role: "delete", label: "删除" },
    { role: "selectAll", label: "全选" },
  ],
});

const viewMenu = (o: MenuOptions): MenuItemConstructorOptions => ({
  label: "显示",
  submenu: [
    { role: "reload", label: "重新加载" },
    { role: "forceReload", label: "强制重新加载" },
    ...(o.devTools ? [{ role: "toggleDevTools", label: "开发者工具" } as const] : []),
    sep,
    { role: "resetZoom", label: "实际大小" },
    { role: "zoomIn", label: "放大" },
    { role: "zoomOut", label: "缩小" },
    sep,
    { role: "togglefullscreen", label: "全屏" },
  ],
});

const windowMenu = (o: MenuOptions): MenuItemConstructorOptions => ({
  label: "窗口",
  submenu:
    o.platform === "darwin"
      ? [{ role: "minimize", label: "最小化" }, { role: "zoom", label: "缩放" }, sep, { role: "front", label: "全部置于顶层" }]
      : [{ role: "minimize", label: "最小化" }, { role: "close", label: "关闭" }],
});

const helpMenu = (o: MenuOptions): MenuItemConstructorOptions => ({
  role: "help",
  label: "帮助",
  submenu: [
    { label: "在浏览器中打开", click: o.openWebsite },
    ...(o.platform !== "darwin" && o.checkForUpdates ? [{ label: "检查更新…", click: o.checkForUpdates }] : []),
    ...(o.platform !== "darwin" ? [sep, { role: "about", label: `关于 ${o.appName}` } as const] : []),
  ],
});

export function menuTemplate(o: MenuOptions): MenuItemConstructorOptions[] {
  const common = [fileMenu(o), editMenu(o), viewMenu(o), windowMenu(o), helpMenu(o)];
  return o.platform === "darwin" ? [appMenu(o), ...common] : common;
}

/** DevTools shortcut that works in packaged builds too: Cmd+Alt+I (Mac), Ctrl+Shift+I (others). */
export function isDevToolsShortcut(input: { type: string; key: string; control: boolean; meta: boolean; alt: boolean; shift: boolean }, platform: NodeJS.Platform): boolean {
  if (input.type !== "keyDown" || input.key.toLowerCase() !== "i") return false;
  return platform === "darwin" ? input.meta && input.alt : input.control && input.shift;
}
