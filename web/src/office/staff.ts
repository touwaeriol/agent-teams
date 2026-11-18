// Staff desks (shared-members.md §9, office-layout.md §4.5): every project member has a fixed
// desk in `staffArea`, rows by role in role-template order, then by instance. Pure.
import type { DeskInstance, SeatAssignment } from "./compile";
import { addEdge, addNode, type NavGraph } from "./nav";
import { isMember, sortMembers } from "./people";
import type { OfficeEmployee, OfficeLayout, Point, StaffArea } from "./types";

export interface StaffPlan {
  rows: OfficeEmployee[][];
  scale: number;
  /** Members beyond the last desk (overflow `stack`): they stand next to it. */
  extra: OfficeEmployee[];
}

export interface StaffPlacement {
  desks: Map<string, DeskInstance>;
  seats: Map<string, SeatAssignment>;
}

const MIN_SCALE = 0.5;
const STACK_STEP = 6;

export const staffDeskKey = (employeeId: string): string => `staff.${employeeId}`;
export const staffRowNode = (row: number): string => `staff.row${row}`;

/** Places every member at its desk, adds the row walkways to the graph. No staff area: nothing. */
export function placeStaff(layout: OfficeLayout, employees: OfficeEmployee[], graph: NavGraph): StaffPlacement {
  const out: StaffPlacement = { desks: new Map(), seats: new Map() };
  const area = layout.staffArea;
  if (!area) return out;
  const plan = planStaff(area, sortMembers(employees.filter(isMember), area.order ?? ["roleSort", "instance"]));
  plan.rows.forEach((row, r) => {
    const node = rowNode(area, graph, r, plan.scale);
    row.forEach((e, i) => placeDesk(layout, area, out, e, cellAt(area, r, i, plan.scale), plan.scale, node));
  });
  stackExtra(area, out, plan);
  return out;
}

/** Rows for the members: grouped by role when that fits, else packed, else scaled or stacked. */
export function planStaff(area: StaffArea, members: OfficeEmployee[]): StaffPlan {
  for (let s = 1; s >= MIN_SCALE - 1e-9; s = round2(s - 0.05)) {
    const rows = fittingRows(area, members, s);
    if (rows) return { rows, scale: s, extra: [] };
    if ((area.overflow ?? "scale") === "stack") break;
  }
  const scale = (area.overflow ?? "scale") === "stack" ? 1 : MIN_SCALE;
  const { perRow, maxRows } = capacity(area, scale);
  const fit = members.slice(0, perRow * maxRows);
  return { rows: chunk(fit, perRow), scale, extra: members.slice(fit.length) };
}

function fittingRows(area: StaffArea, members: OfficeEmployee[], scale: number): OfficeEmployee[][] | undefined {
  const { perRow, maxRows } = capacity(area, scale);
  const grouped = (area.group ?? "role") === "role" ? byRole(members).flatMap((g) => chunk(g, perRow)) : undefined;
  if (grouped && grouped.length <= maxRows) return grouped;
  const packed = chunk(members, perRow);
  return packed.length <= maxRows ? packed : undefined;
}

export function capacity(area: StaffArea, scale: number): { perRow: number; maxRows: number } {
  const [, , w, h] = area.rect;
  const [cw, ch] = area.cell;
  const perRow = area.perRow === undefined || area.perRow === "auto" ? Math.floor(w / (cw * scale) + 1e-9) : area.perRow;
  return { perRow: Math.max(1, perRow), maxRows: Math.max(1, Math.floor(h / (ch * scale) + 1e-9)) };
}

function byRole(members: OfficeEmployee[]): OfficeEmployee[][] {
  const groups: OfficeEmployee[][] = [];
  for (const e of members) {
    const last = groups[groups.length - 1];
    if (last && last[0].role === e.role) last.push(e);
    else groups.push([e]);
  }
  return groups;
}

function chunk<T>(list: T[], n: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < list.length; i += n) out.push(list.slice(i, i + n));
  return out;
}

function cellAt(area: StaffArea, row: number, col: number, s: number): Point {
  return [area.rect[0] + col * area.cell[0] * s, area.rect[1] + row * area.cell[1] * s];
}

/** The row's walkway node on the spine at the seat line, chained to the previous row (or the entry). */
function rowNode(area: StaffArea, g: NavGraph, row: number, s: number): string {
  const id = staffRowNode(row);
  addNode(g, id, [area.nav.spineX, area.rect[1] + (row * area.cell[1] + area.desk.seat[1]) * s]);
  addEdge(g, id, row === 0 ? area.nav.entry : staffRowNode(row - 1));
  return id;
}

function placeDesk(layout: OfficeLayout, area: StaffArea, out: StaffPlacement, e: OfficeEmployee, cell: Point, s: number, node: string): void {
  const sprite = layout.sprites[area.desk.sprite];
  const local = area.desk.at ?? [0, 0];
  const at: Point = [cell[0] + local[0] * s, cell[1] + local[1] * s];
  const { timer: t, tag } = area.desk;
  const desk: DeskInstance = {
    id: staffDeskKey(e.id),
    key: staffDeskKey(e.id),
    deskId: "staff",
    sprite,
    at,
    z: 0,
    slots: { ...(sprite?.slots ?? {}) },
    scale: s,
    seat: [cell[0] + area.desk.seat[0] * s, cell[1] + area.desk.seat[1] * s],
    timer: t && [at[0] + t[0] * s, at[1] + t[1] * s],
    owner: e.id,
    tag: tag && [at[0] + tag[0] * s, at[1] + tag[1] * s],
  };
  out.desks.set(e.id, desk);
  out.seats.set(e.id, { point: desk.seat, deskKey: desk.key, accessNode: node });
}

function stackExtra(area: StaffArea, out: StaffPlacement, plan: StaffPlan): void {
  const last = [...out.desks.values()].pop();
  const base: Point = last?.seat ?? [area.rect[0], area.rect[1]];
  const node = plan.rows.length ? staffRowNode(plan.rows.length - 1) : area.nav.entry;
  plan.extra.forEach((e, i) => out.seats.set(e.id, { point: [base[0] + (i + 1) * STACK_STEP, base[1] + (i + 1) * STACK_STEP], accessNode: node }));
}

function round2(v: number): number {
  return Math.round(v * 100) / 100;
}
