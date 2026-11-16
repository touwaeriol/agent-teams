import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { OfficeEmployee, OfficeLayout, OfficeThread } from "../types";

export const repoPath = (rel: string) => fileURLToPath(new URL(`../../../../${rel}`, import.meta.url));

export function readJson<T = unknown>(rel: string): T {
  return JSON.parse(readFileSync(repoPath(rel), "utf8")) as T;
}

export function defaultLayout(): OfficeLayout {
  return readJson<OfficeLayout>("office/default.json");
}

export const twoThreads: OfficeThread[] = [
  { no: 12, title: "员工通信与审批", status: "running", counts: { main_done: 7, main_total: 13 } },
  { no: 13, title: "Codex 员工接入", status: "running", counts: { main_done: 3, main_total: 6 } },
];

export const crew: OfficeEmployee[] = [
  { id: "coord", name: "leader", tier: "coordinator", role: "coordinator", thread_no: null },
  { id: "lead-12", name: "lead-12", tier: "lead", role: "lead", thread_no: 12 },
  { id: "dev-1", name: "dev-1", tier: "member", role: "dev", thread_no: 12 },
  { id: "dev-2", name: "dev-2", tier: "member", role: "dev", thread_no: 12 },
  { id: "dev-3", name: "dev-3", tier: "member", role: "dev", thread_no: 12 },
  { id: "qa-2", name: "qa-2", tier: "member", role: "qa", thread_no: 12 },
  { id: "lead-13", name: "lead-13", tier: "lead", role: "lead", thread_no: 13 },
  { id: "qa-1", name: "qa-1", tier: "member", role: "qa", thread_no: 13 },
];
