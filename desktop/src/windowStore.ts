// Reads and writes the window state file in userData (the pure parts are in windowState.ts).
import { BrowserWindow, app, screen } from "electron";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { parseWindowState, restoreBounds, type WindowState } from "./windowState";

const file = () => join(app.getPath("userData"), "window-state.json");

function read(): string | null {
  try {
    return readFileSync(file(), "utf8");
  } catch {
    return null;
  }
}

export function loadWindowState() {
  const state = parseWindowState(read());
  return { bounds: restoreBounds(state.bounds, screen.getAllDisplays().map((d) => d.workArea)), maximized: state.maximized === true };
}

/** Saves the window's normal bounds whenever it moves, resizes or closes. */
export function trackWindowState(win: BrowserWindow) {
  let timer: NodeJS.Timeout | undefined;
  const save = () => {
    clearTimeout(timer);
    if (win.isDestroyed()) return;
    const state: WindowState = { bounds: win.getNormalBounds(), maximized: win.isMaximized() };
    try {
      writeFileSync(file(), JSON.stringify(state));
    } catch {
      // Not worth bothering the user about.
    }
  };
  const later = () => {
    clearTimeout(timer);
    timer = setTimeout(save, 500);
  };
  win.on("resize", later);
  win.on("move", later);
  win.on("close", save);
}
