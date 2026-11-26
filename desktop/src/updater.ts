// Auto-update from GitHub Releases (publish config in electron-builder.config.cjs).
// Only packaged builds check; unsigned Mac builds cannot install updates (Squirrel.Mac
// requires a signature), so a failure is logged and the app keeps running.
import { app, dialog } from "electron";
import { autoUpdater } from "electron-updater";

export const updatesEnabled = (env: Record<string, string | undefined>) => app.isPackaged && env.AGENT_TEAMS_NO_UPDATE !== "1";

export function startAutoUpdate() {
  autoUpdater.on("error", (err) => console.warn("[updater]", err?.message ?? err));
  void autoUpdater.checkForUpdatesAndNotify().catch((err: unknown) => console.warn("[updater]", err));
}

/** 检查更新… from the menu: checks now and says what it found. */
export async function checkForUpdatesNow() {
  try {
    const result = await autoUpdater.checkForUpdates();
    const latest = result?.updateInfo.version;
    const message = latest && latest !== app.getVersion() ? `发现新版本 ${latest}，正在后台下载，下载完成后退出 App 时安装。` : `已经是最新版本（${app.getVersion()}）。`;
    await dialog.showMessageBox({ type: "info", message });
  } catch (err) {
    await dialog.showMessageBox({ type: "warning", message: "检查更新失败", detail: err instanceof Error ? err.message : String(err) });
  }
}
