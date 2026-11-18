// Office layout types (docs/v2/office-layout.md). The JSON Schema is the source
// of truth; these hand-written types cover what the renderer reads.

export type Point = [number, number];
export type Rect = [number, number, number, number];
export type Color = string;
export type Cell = [number, number, string];

export interface Shape {
  type: "rect" | "ellipse";
  x: number;
  y: number;
  w: number;
  h: number;
  fill?: Color;
  stroke?: Color;
  strokeWidth?: number;
  radius?: number | [number, number, number, number];
}

export interface Sprite {
  kind: "shapes" | "pixels" | "image";
  w: number;
  h: number;
  slots?: Record<string, Color>;
  shapes?: Shape[];
  pixel?: number;
  rows?: string[];
  colors?: Record<string, Color>;
  src?: string;
  frames?: number;
  fps?: number;
}

export interface Floor {
  base: Color;
  pattern: "plain" | "planks" | "grid" | "checker" | "image";
  line?: Color;
  line2?: Color;
  stepX?: number;
  stepY?: number;
  lineWidth?: number;
  src?: string;
}

export interface Animation {
  frames: string[];
  fps?: number;
  bob?: number;
}

export interface Overlay {
  cells: Cell[];
  blinkFps?: number;
}

export interface Character {
  pixel: number;
  width: number;
  anchor?: { x?: number; y?: number };
  colors: Record<string, Color>;
  parts: Record<string, string[]>;
  frames: Record<string, string[]>;
  animations: Record<string, Animation>;
  overlays?: Record<string, Overlay>;
}

export interface LookPiece {
  label: string;
  colors?: Record<string, Color>;
  patch?: Record<string, Record<string, string>>;
  cells?: Cell[];
}

export interface Look {
  color: string;
  outfit: string;
  accessory: string;
  hair?: string | null;
}

export interface Looks {
  outfits: Record<string, LookPiece>;
  accessories: Record<string, LookPiece>;
  colors: string[];
  hair: string[];
  skin?: string[];
  variation?: { hair?: "byInstance" | "fixed" | "random"; shirtShade?: number[] };
  defaults: Record<string, Look>;
}

export interface Door {
  side: "n" | "s" | "e" | "w";
  offset: number;
  width?: number;
  draw?: boolean;
}

export interface Room {
  id: string;
  label: string;
  kind: string;
  rect: Rect;
  floor: string;
  doors?: Door[];
  wall?: { color?: Color; width?: number; style?: "solid" | "dashed" };
  /** Name plate colours; `offset` from the room's top-left corner (default `labels.room.offset`). */
  labelStyle?: { bg?: Color; fg?: Color; hidden?: boolean; offset?: Point };
  z?: number;
}

export interface Furniture {
  id: string;
  sprite: string;
  at: Point;
  room?: string;
  z?: number;
  flip?: boolean;
  label?: string;
  slots?: Record<string, Color>;
}

export interface TextStyle {
  value?: string;
  at?: Point;
  offset?: Point;
  font?: "label" | "bubble";
  size?: number;
  color?: Color;
  bg?: Color;
  border?: Color;
  padding?: Point;
  maxChars?: number;
  dot?: Color;
}

export interface SlotColor {
  led?: Color;
  led2?: Color;
  blink?: "led" | "led2";
}

export interface PipelineSection {
  kind: "mergeQueue" | "ci";
  title: string;
  maxItems?: number;
  row?: string;
  stateLabels?: Record<string, string>;
  stateColors?: Record<string, Color>;
  colors?: Record<string, Color>;
  blink?: string[];
}

export interface Widget {
  id: string;
  type: "taskBoard" | "nodeRacks" | "pipelinePanel";
  at: Point;
  room?: string;
  z?: number;
  click?: "openTaskPool" | "openNode" | "openMergeQueue" | "none";
  sprite?: string;
  text?: TextStyle;
  grid?: { columns: number; stepX: number; stepY: number; maxItems?: number };
  slot?: { sprite: string; at: Point; stepY: number; max: number };
  slotColors?: Partial<Record<"active" | "parked" | "queued" | "empty" | "offline", SlotColor>>;
  label?: TextStyle;
  overflowLabel?: string;
  w?: number;
  style?: { bg?: Color; border?: Color; color?: Color; title?: Color; size?: number; padding?: number; gap?: number };
  sections?: PipelineSection[];
  /** taskBoard: the project queue as cards (thread colour, lock, reserved-for name). */
  cards?: BoardCards;
}

export interface BoardCards {
  at: Point;
  size: Point;
  gap?: Point;
  columns: number;
  max: number;
  text?: TextStyle;
  color?: Color;
}

export interface DeskDef {
  id: string;
  sprite: string;
  at: Point;
  seat: Point;
  for?: "lead" | "member" | "any";
  timer?: Point;
}

export interface PodTemplate {
  size: Point;
  frame?: { color?: Color; focusColor?: Color; width?: number; style?: "solid" | "dashed" };
  plate?: TextStyle;
  desks: DeskDef[];
  overflowSeats?: Point[];
  /** Small avatars of the members working for this thread now. */
  participants?: { at: Point; size?: number; step?: Point; max: number };
  /** Where visitors stand (place `pod`); default the first exit node. */
  visit?: Point;
  nav: {
    nodes: Record<string, Point>;
    edges: [string, string][];
    seatLinks: Record<string, string>;
    exits: Record<string, string>;
  };
}

export type ThreadStatus = "queued" | "running" | "waiting" | "review" | "paused" | "done" | "failed" | "archived";

export interface ThreadPods {
  area: { room?: string; rect: Rect };
  templates: Record<string, PodTemplate>;
  layout: {
    mode: "grid";
    gap?: Point;
    columns?: number | "auto";
    use: { maxThreads: number; template: string }[];
    overflow?: "paginate" | "scale";
    offPage?: "hide" | "anchorsWithBadge";
    order?: "no" | "updated" | "status";
    pinFocused?: boolean;
    show?: ThreadStatus[];
  };
  seating: { leadDesk?: string; order?: ("roleSort" | "instance" | "name")[]; rowOf?: Record<string, string> };
  threadColors?: string[];
  /** How long an ended thread's table stays, fading, while its lead leaves. */
  fadeMs?: number;
}

/** The members' fixed desks, in rows by role (shared-members.md §9). */
export interface StaffArea {
  room?: string;
  rect: Rect;
  desk: { sprite: string; at?: Point; seat: Point; timer?: Point; tag?: Point };
  cell: Point;
  perRow?: number | "auto";
  group?: "role" | "none";
  order?: ("roleSort" | "instance" | "name")[];
  overflow?: "scale" | "stack";
  nav: { entry: string; spineX: number };
}

export interface Anchor {
  points: Point[];
  pose?: "stand" | "sit";
  facing?: "up" | "down" | "left" | "right";
  nav?: string;
  overflow?: "jitter" | "standBehind" | "stack";
}

export type StateName =
  | "typing" | "reading" | "running" | "thinking" | "walking_to_board" | "claiming" | "returning"
  | "raising_hand" | "idle" | "waiting" | "queued" | "offline" | "broken";

export type BubbleKind = "plain" | "ask" | "error" | "done";

export interface StateRule {
  match: { state?: StateName[]; substate?: string[]; tier?: string; role?: string[]; taskKind?: string[] };
  place: string;
  anim?: string;
  overlay?: string;
  screen?: Color;
  timer?: boolean;
  bubble?: string;
  bubbleKind?: BubbleKind;
  opacity?: number;
  grayscale?: boolean;
}

export interface SequenceStep {
  goto?: string;
  wait?: number;
  anim?: string;
  bubble?: string;
  /** From this step on the person is not drawn. */
  hide?: boolean;
}

export type SceneEvent = "threadOpened" | "threadEnded";

export interface Sequence {
  trigger: { state?: StateName; cause?: string; event?: SceneEvent; tier?: "coordinator" | "lead" | "member" };
  steps: SequenceStep[];
}

export interface States {
  rules: StateRule[];
  sequences?: Record<string, Sequence>;
  walkAnim?: string;
  maxConcurrentWalks?: number;
}

export interface OfficeLayout {
  $schema?: string;
  format: 1;
  id: string;
  name: string;
  extends?: string;
  canvas: {
    width: number;
    height: number;
    pixel: number;
    background?: Color;
    frame?: { color?: Color; width?: number; radius?: number };
    page?: Color;
  };
  fonts?: { label?: string; bubble?: string; googleFonts?: string[] };
  palette?: Record<string, string>;
  floors?: Record<string, Floor>;
  sprites: Record<string, Sprite>;
  characters: { person: Character } & Record<string, Character>;
  looks: Looks;
  rooms: Room[];
  furniture?: Furniture[];
  widgets?: Widget[];
  threadPods: ThreadPods;
  staffArea?: StaffArea;
  members?: { visitPods?: boolean };
  anchors: Record<string, Anchor>;
  nav: { walkSpeed?: number; nodes: Record<string, Point>; edges: [string, string][] };
  states: States;
  labels?: { room?: TextStyle; nameTag?: TextStyle; timer?: TextStyle };
  bubbles?: {
    font?: "label" | "bubble";
    size?: number;
    maxChars?: number;
    offset?: Point;
    shadow?: string;
    ttlMs?: number;
    kinds?: Partial<Record<BubbleKind, { bg: Color; border: Color; color?: Color }>>;
  };
  focus?: { dimOpacity?: number; clickPlate?: "focusThread" | "openThread" | "none" };
  legend?: { label: string; color: Color }[];
  meta?: Record<string, unknown>;
}

// ---- live data the renderer consumes (shapes follow docs/v2/api.md views) ----

/** EmployeeStatusView (api.md §2) plus the optional `cause` of the status event. */
export interface EmployeeStatus {
  state: StateName;
  substate?: string;
  bubble?: string;
  bubble_kind?: BubbleKind | "";
  task_id?: string;
  task_kind?: string;
  tier?: string;
  role?: string;
  /** ISO time or epoch ms when this state started (desk timer). */
  since?: string | number;
  seq?: number;
  /** at_status_events.cause, e.g. "task.claimed:t8"; triggers sequences. */
  cause?: string;
}

/** EmployeeBrief + thread_no (the office snapshot's employee shape). */
export interface OfficeEmployee {
  id: string;
  name: string;
  tier: "coordinator" | "lead" | "member" | string;
  role: string;
  /** The thread this employee works in; null/undefined for the coordinator. */
  thread_no?: number | null;
  look?: Partial<Look> | null;
  status?: EmployeeStatus | null;
  /** Instance number among same-role employees (defaults to the trailing number of `name`). */
  instance?: number;
  /** Role order from the team template (seating `roleSort`). */
  sort?: number;
  /** Role title from the team template. */
  title?: string;
}

/** ThreadView subset. */
export interface OfficeThread {
  no: number;
  title: string;
  status: ThreadStatus;
  updated_at?: string;
  counts?: { main_done?: number; main_total?: number };
}

/** A status event: the SSE `status` frame or one row of the replay `events`. */
export interface OfficeStatusEvent {
  seq: number;
  /** ISO time or epoch ms. */
  at: string | number;
  employee: string | { id: string };
  status: EmployeeStatus;
}

export interface OfficeNode {
  id: string;
  name: string;
  online: boolean;
  capacity: number;
  active: number;
  parked?: number;
  queued?: number;
}

export interface OfficeMergeEntry {
  thread_no: number;
  employee: string;
  task_id?: string;
  status: string;
}

export interface OfficeCheck {
  name: string;
  status: string;
  conclusion?: string | null;
}

/** One pending task of the project queue (the task board's cards). */
export interface OfficeQueueCard {
  thread_no: number;
  id: string;
  role?: string;
  /** Some dependency is not done yet. */
  locked?: boolean;
  /** Rework kept for this member until the reservation runs out. */
  reserved_for?: string;
  /** Named assignee (empty: anyone of the role). */
  assignee?: string;
}

export interface OfficeWidgetData {
  board?: { claimable: number; by_thread?: Record<string, Record<string, number>>; queue?: OfficeQueueCard[] };
  nodes?: OfficeNode[];
  mergeQueue?: OfficeMergeEntry[];
  ci?: OfficeCheck[];
}
