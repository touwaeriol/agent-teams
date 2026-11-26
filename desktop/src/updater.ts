// Auto-update from GitHub Releases (publish config in electron-builder.config.cjs).
// Only packaged builds check. An unsigned Mac build cannot install updates (Squirrel.Mac requires a
// signature), so it does not download them: it says a new version exists and opens the release page.
import { app, dialog, shell } from "electron";
import { autoUpdater } from "electron-updater";
import { canInstallUpdates } from "./config";

const RELEASES_URL = "https://github.com/touwaeriol/agent-teams/releases";

export const updatesEnabled = (env: Record<string, string | undefined>) => app.isPackaged && env.AGENT_TEAMS_NO_UPDATE !== "1";

const installs = () => canInstallUpdates(process.platform, require("../package.json") as { macSigned?: boolean });

export function startAutoUpdate() {
  autoUpdater.on("error", (err) => console.warn("[updater]", err?.message ?? err));
  if (!installs()) {
    autoUpdater.autoDownload = false;
    return;
  }
  void autoUpdater.checkForUpdatesAndNotify().catch((err: unknown) => console.warn("[updater]", err));
}

/** 检查更新… from the menu: checks now and says what it found. */
export async function checkForUpdatesNow() {
  try {
    const result = await autoUpdater.checkForUpdates();
    if (!result?.isUpdateAvailable) {
      await dialog.showMessageBox({ type: "info", message: `已经是最新版本（${app.getVersion()}）。` });
      return;
    }
    const latest = result.updateInfo.version;
    if (installs()) {
      await dialog.showMessageBox({ type: "info", message: `发现新版本 ${latest}，正在后台下载，下载完成后退出 App 时安装。` });
      return;
    }
    const { response } = await dialog.showMessageBox({ type: "info", message: `发现新版本 ${latest}`, detail: "这个版本没有签名，不能自动更新，请到发布页下载安装。", buttons: ["打开发布页", "以后再说"] });
    if (response === 0) void shell.openExternal(RELEASES_URL);
  } catch (err) {
    await dialog.showMessageBox({ type: "warning", message: "检查更新失败", detail: err instanceof Error ? err.message : String(err) });
  }
}
