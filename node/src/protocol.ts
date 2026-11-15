// Types and validation for the v2 node protocol; /protocol/schemas is the single source of truth.
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import type { ValidateFunction } from "ajv";
import bundle from "../../protocol/gen/bundle.schema.json" with { type: "json" };
import manifest from "../../protocol/gen/manifest.json" with { type: "json" };
import { MESSAGE_META, type MessageBodies, type MessageType } from "../../protocol/gen/ts/index.js";

export * from "../../protocol/gen/ts/index.js";
export type Body<K extends MessageType> = MessageBodies[K];

export interface Frame {
  v: 2;
  type: string;
  id: string;
  seq?: number;
  ts: string;
  trace?: string;
  body: Record<string, unknown>;
}

export function isMessageType(type: string): type is MessageType {
  return Object.prototype.hasOwnProperty.call(MESSAGE_META, type);
}

export function isReliable(type: MessageType): boolean {
  return MESSAGE_META[type].reliable;
}

/** Idempotency key of a reliable command: type + the x-key fields (protocol.md §2.2). */
export function commandKey(frame: Frame): string {
  if (!isMessageType(frame.type)) return `${frame.type}:${frame.id}`;
  const fields = MESSAGE_META[frame.type].key;
  if (fields.length === 0) return `${frame.type}:${frame.id}`;
  const parts = fields.map((f) => (f === "id" ? frame.id : String(frame.body[f] ?? "")));
  return `${frame.type}:${parts.join(":")}`;
}

export type ValidationResult = { ok: true } | { ok: false; code: "unsupported" | "bad_schema"; message: string };

/** ajv validators for inbound console frames (envelope + body by type). */
export class ProtocolValidator {
  private readonly envelope: ValidateFunction;
  private readonly bodies = new Map<string, ValidateFunction>();

  constructor() {
    const ajv = new Ajv2020({ strict: false, allErrors: false, discriminator: true });
    addFormats(ajv);
    ajv.addSchema({ ...bundle, $id: "bundle" });
    this.envelope = ajv.getSchema("bundle")!;
    for (const m of manifest.messages) this.bodies.set(m.type, ajv.getSchema(`bundle#/$defs/${m.name}`)!);
  }

  validateInbound(frame: unknown): ValidationResult {
    if (!this.envelope(frame)) return { ok: false, code: "bad_schema", message: errorText(this.envelope) };
    const f = frame as Frame;
    if (!isMessageType(f.type)) return { ok: false, code: "unsupported", message: `unknown type ${f.type}` };
    if (MESSAGE_META[f.type].direction === "node_to_console") return { ok: false, code: "unsupported", message: `${f.type} is node->console` };
    return this.validateBody(f);
  }

  private validateBody(f: Frame): ValidationResult {
    const reliable = MESSAGE_META[f.type as MessageType].reliable;
    if (reliable !== (typeof f.seq === "number")) return { ok: false, code: "bad_schema", message: reliable ? "missing seq" : "unexpected seq" };
    const v = this.bodies.get(f.type)!;
    return v(f.body) ? { ok: true } : { ok: false, code: "bad_schema", message: errorText(v) };
  }
}

function errorText(v: ValidateFunction): string {
  return (v.errors ?? []).map((e) => `${e.instancePath || "/"} ${e.message}`).join("; ");
}
