import { z } from "zod";

type Shape = Record<string, z.ZodType>;

/**
 * Converts a platform tool's JSON Schema (from session.open) into the zod raw shape the
 * Agent SDK's tool() wants. The console validates the arguments again, so if a schema
 * cannot be converted the tool still loads with loosely typed properties.
 */
export function zodShape(schema: Record<string, unknown>): Shape {
  try {
    const converted = z.fromJSONSchema(schema as never);
    if (converted instanceof z.ZodObject) return converted.shape as Shape;
  } catch {
    // fall through to the loose shape
  }
  return looseShape(schema);
}

function looseShape(schema: Record<string, unknown>): Shape {
  const props = (schema.properties ?? {}) as Record<string, { description?: string }>;
  const required = new Set((schema.required ?? []) as string[]);
  const shape: Shape = {};
  for (const [name, prop] of Object.entries(props)) {
    const base = z.any().describe(prop.description ?? name);
    shape[name] = required.has(name) ? base : base.optional();
  }
  return shape;
}
