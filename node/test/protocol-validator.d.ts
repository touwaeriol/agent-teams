declare module "*/protocol/scripts/validator.mjs" {
  export function createAjv(opts?: { formats?: boolean }): unknown;
  export function messageValidator(ajv: unknown): ((v: unknown) => boolean) & { errors?: unknown };
  export function describeErrors(v: unknown): string;
}
