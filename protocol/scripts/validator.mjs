// ajv (draft 2020-12) loaded with every schema under protocol/schemas.
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { schemaFiles, loadSchema, schemaId, toolSchemas } from "./lib.mjs";

const VOCABULARY = ["x-direction", "x-reliable", "x-key", "x-tool"];
const OFFICE = "office/office-layout.schema.json";

export function createAjv({ formats = true } = {}) {
  const ajv = new Ajv2020({ strict: true, strictRequired: false, allErrors: true, discriminator: true, validateFormats: formats });
  addFormats(ajv);
  ajv.addVocabulary(VOCABULARY);
  for (const rel of [...schemaFiles("node"), ...schemaFiles("tools")]) ajv.addSchema(loadSchema(rel));
  ajv.addSchema(loadSchema(OFFICE));
  return ajv;
}

export function messageValidator(ajv) {
  return ajv.getSchema(schemaId("node/message.json"));
}

export function bodyValidator(ajv, type) {
  return ajv.getSchema(schemaId(`node/${type}.json`));
}

export function toolValidator(ajv, name) {
  return ajv.getSchema(schemaId(`tools/${name}.json`));
}

export function officeValidator(ajv) {
  return ajv.getSchema(loadSchema(OFFICE).$id);
}

export function toolNames() {
  return toolSchemas().map((t) => t.name);
}

export function describeErrors(validate) {
  return (validate.errors ?? []).map((e) => `${e.instancePath || "/"} ${e.message}`).join("; ");
}
