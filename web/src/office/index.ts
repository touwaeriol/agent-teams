// Public API of the office renderer. See README.md.
export { Office, pickScale, type OfficeProps } from "./Office";
export type { OfficeAction } from "./overlay";
export { useHotLayout, useRemoteLayout, hotLayout, type HotLayout, type RemoteLayout } from "./useLayout";
export { useReplay, REPLAY_SPEEDS, type ReplayState } from "./replay";
export { default as ReplayBar } from "./ReplayBar.vue";
export { loadLayout, validateLayout, schemaProblems, mergeLayouts, LayoutError } from "./load";
export { validateSemantics } from "./validate";
export { resolveLook, composeFrame, frameBitmap } from "./looks";
export { BUILTIN_LAYOUT } from "./builtin";
export type * from "./types";
