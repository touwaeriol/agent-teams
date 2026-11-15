import { createHash, randomBytes } from "node:crypto";
import { monotonicFactory } from "ulid";

const nextUlid = monotonicFactory();

/** Prefixed ULID, e.g. newId("n_") for daemon envelope ids, newId("tc_") for tool calls. */
export function newId(prefix: string): string {
  return prefix + nextUlid();
}

export function sha256Hex(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}

/** approval_id = ap_ + sha256(session_id + ':' + tool_use_id)[:24] (protocol.md §5.1). */
export function approvalId(sessionId: string, toolUseId: string): string {
  return "ap_" + sha256Hex(`${sessionId}:${toolUseId}`).slice(0, 24);
}

/**
 * Deterministic RFC 4122 v5-shaped UUID for a console msg_id. The SDK wants a UUID in
 * SDKUserMessage.uuid, while msg_id is m_ + ULID; the mapping lets the daemon find
 * already-delivered messages in a transcript after a restart (protocol.md §2.2).
 */
export function msgUuid(msgId: string): string {
  const h = createHash("sha1").update("agent-teams:msg:" + msgId).digest();
  h[6] = (h[6]! & 0x0f) | 0x50;
  h[8] = (h[8]! & 0x3f) | 0x80;
  const x = h.subarray(0, 16).toString("hex");
  return `${x.slice(0, 8)}-${x.slice(8, 12)}-${x.slice(12, 16)}-${x.slice(16, 20)}-${x.slice(20, 32)}`;
}

export function randomSecret(bytes = 32): string {
  return randomBytes(bytes).toString("base64url");
}
