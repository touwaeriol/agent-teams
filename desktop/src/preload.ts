// Sandboxed preload: exposes read-only facts about the shell, nothing that reaches Node.
import { contextBridge } from "electron";

const arg = (name: string) => process.argv.find((a) => a.startsWith(`--${name}=`))?.slice(name.length + 3) ?? "";

contextBridge.exposeInMainWorld("agentTeamsDesktop", Object.freeze({ version: arg("agent-teams-version"), platform: process.platform }));
