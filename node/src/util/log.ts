import pino from "pino";

export type Logger = pino.Logger;

export function createLogger(level: string = process.env.AGENT_TEAMS_LOG_LEVEL ?? "info"): Logger {
  return pino({ level, base: { svc: "agent-teams-node" } }, pino.destination(2));
}

export const silentLogger: Logger = pino({ level: "silent" });
