// Phase 6 orchestration models (docs/API-orchestration.md). Field names are the exact
// JSON names. Optional / nullable where the contract says so or where a hub may omit them.

import type { Session } from '../lib/types';

// ---- §1 board kinds -------------------------------------------------------------

export const ROLES = ['general', 'frontend', 'backend', 'testers', 'reviewers', 'design', 'docs', 'security'] as const;
export type Role = (typeof ROLES)[number];
export const ROLE_LABEL: Record<Role, string> = {
  general: 'General',
  frontend: 'Frontend',
  backend: 'Backend',
  testers: 'Testers',
  reviewers: 'Reviewers',
  design: 'Design',
  docs: 'Docs',
  security: 'Security',
};

export const GROUP_COLORS = ['amber', 'sage', 'sky', 'violet', 'rose', 'teal'] as const;
export type GroupColor = (typeof GROUP_COLORS)[number];
/** Default colour per role (§1). */
export const ROLE_COLOR: Record<Role, GroupColor> = {
  general: 'sky',
  frontend: 'violet',
  backend: 'teal',
  testers: 'amber',
  reviewers: 'rose',
  design: 'violet',
  docs: 'sage',
  security: 'rose',
};

export interface GroupData {
  name: string;
  role: Role;
  color: GroupColor;
  members: string[];
  /** What is typed in one member also goes to the others (§5). */
  syncInput?: boolean;
}

export interface OrchestratorData {
  title: string;
  /** How this card's lead talks and leads; empty = the hub's. */
  personality?: string;
}

/** A memory added to a tile or picked for a send. No projectId: the memory of all projects. */
export interface MemoryBlock {
  projectId?: string;
  name: string;
  /** Left out of what is sent: agents are told it exists and read it when needed. */
  lookup?: boolean;
}

export interface MemoryNodeData {
  blocks: MemoryBlock[];
  /** Nothing is typed when it is wired or a block is added; the owner sends by hand. */
  manual: boolean;
  lastPush?: { at: string; delivered: number; busy: number; skipped: number } | null;
}

export interface MemoryPlanDoc {
  projectId?: string;
  name: string;
  title: string;
  owner: string;
  chars: number;
  locked?: boolean;
  lookup?: boolean;
  missing?: boolean;
}

/** What a send would do to one console. chars 0: it has everything already. */
export interface MemoryPlanTarget {
  sessionId: string;
  handle: string;
  agent: string;
  workspaceId: string;
  state: string;
  canSend: boolean;
  chars: number;
  lookup: string[];
}

export interface MemoryPlan {
  docs: MemoryPlanDoc[];
  targets: MemoryPlanTarget[];
  budget: number;
}

export type ForwardMode = 'digest' | 'split';
export type ForwardTrigger = 'turn' | 'order';

// ---- §2 delivery ---------------------------------------------------------------

export type DeliveryState = 'queued' | 'waiting' | 'typing' | 'sent' | 'confirmed' | 'failed' | 'expired' | 'cancelled';
export type DeliverySource = 'order' | 'loop' | 'memory' | 'handoff' | 'forward' | 'reply' | 'skill' | 'dictation' | 'api';

export interface Delivery {
  id: string;
  sessionId: string;
  handle: string;
  text: string;
  submit: boolean;
  source: DeliverySource | string;
  guard?: 'agent' | 'dictation';
  orderId?: string;
  forwardDepth?: number;
  state: DeliveryState | string;
  waitReason?: string;
  createdAt: string;
  deadline?: string;
  sentAt?: string | null;
  confirmedAt?: string | null;
  error?: string;
  code?: string;
}

export const DELIVERY_ACTIVE = new Set(['queued', 'waiting', 'typing']);

// ---- §3 commands ---------------------------------------------------------------

export type UiPanel = 'loops' | 'memory' | 'mission' | 'settings' | 'git' | 'usage' | 'documents' | 'code' | 'markdown' | 'skills';

export interface UiAction {
  action: 'openFile' | 'fit' | 'zoomIn' | 'zoomOut' | 'zoomReset' | 'focus' | 'openPanel' | 'closePanel' | 'tool' | 'undo' | 'redo' | 'goWorkspace' | string;
  target?: string;
  panel?: UiPanel | string;
  tool?: 'select' | 'pen' | 'eraser' | string;
  workspaceId?: string;
  /** openFile: a file for the editor card. */
  path?: string;
  line?: number;
}

export interface CommandRequest {
  text: string;
  source?: 'typed' | 'voice';
  workspaceId?: string;
  cardId?: string | null;
  clientId?: string;
  selection?: string[];
  planner?: 'auto' | 'off';
  /** Id of the thread message this answers. */
  replyTo?: string;
}

export interface CommandResponse {
  commandId: string;
  threadId: string;
  handledBy: 'parser' | 'planner' | 'pending' | string;
  orderId?: string | null;
  uiActions?: UiAction[] | null;
  confirmation?: Confirmation | null;
  messages?: ThreadMessage[] | null;
}

export type MentionKind = 'everyone' | 'console' | 'group' | 'role' | 'agent';
export interface Mention {
  kind: MentionKind;
  id: string;
  label: string;
  handle?: string;
  agentState?: string;
  /** Tab name when the mention lives on another board. */
  board?: string;
}

/** Steps carried by confirmations (§3.4); only `op` is relied on by the UI. */
export interface Step {
  op: string;
  targets?: string[];
  text?: string;
  count?: number;
  agent?: string;
  [k: string]: unknown;
}

export type ConfirmationKind = 'destructive' | 'didYouMean' | 'sendToAll' | 'risky' | 'loop' | 'bulkSpawn' | 'proposal';
export interface Confirmation {
  id: string;
  threadId: string;
  commandId: string;
  kind: ConfirmationKind | string;
  prompt: string;
  options: { id: string; label: string }[];
  candidates?: { sessionId: string; handle: string }[] | null;
  steps?: Step[] | null;
  /** A proposal in words. */
  plan?: { handle: string; text: string }[] | null;
  expiresAt: string;
  state: 'pending' | 'confirmed' | 'cancelled' | 'expired' | string;
  decidedBy?: string;
}

export interface PlannerStatus {
  available: boolean;
  reason?: 'claude_missing' | 'not_logged_in' | 'disabled' | 'cooldown' | string;
  lastLatencyMs?: number;
}

// ---- §4 threads & orders ----------------------------------------------------------

export type MessageKind = 'order' | 'plan' | 'report' | 'reply' | 'note' | 'confirm' | 'error';
export interface ThreadMessage {
  id: string;
  threadId: string;
  at: string;
  role: 'user' | 'hub' | 'agent' | string;
  kind: MessageKind | string;
  text: string;
  source?: 'typed' | 'voice' | 'loop' | 'api' | string;
  orderId?: string;
  sessionId?: string;
  handle?: string;
  plan?: { handle: string; text: string }[] | null;
  report?: { state: 'finished' | 'failed' | 'timeout' | 'skipped' | string; durationMs?: number; excerpt?: string; chars?: number } | null;
  confirmationId?: string;
  /** The earlier message this answers. */
  replyTo?: { id: string; handle?: string; excerpt: string } | null;
  /** Offer to send orderId again while one of its targets is undelivered. */
  resend?: boolean;
}

/** The reply a report message stands for (GET /api/threads/{tid}/messages/{id}/output). */
export interface MessageOutput {
  messageId: string;
  threadId: string;
  sessionId?: string;
  handle?: string;
  source?: 'transcript' | 'screen';
  text: string;
  at: string;
  chars: number;
  truncated: boolean;
  /** From before replies were kept: only the part in the message is left. */
  partial?: boolean;
}

export type OrderTargetState = 'queued' | 'delivered' | 'working' | 'needsYou' | 'finished' | 'failed' | 'timeout' | 'cancelled' | 'skipped';
export interface OrderTarget {
  sessionId: string;
  handle: string;
  deliveryId?: string;
  state: OrderTargetState | string;
  startedAt?: string | null;
  finishedAt?: string | null;
  error?: string;
  /** A hub restart kept the text from this console. */
  undelivered?: boolean;
}
export interface Order {
  id: string;
  threadId: string;
  commandId?: string;
  text: string;
  createdAt: string;
  targets: OrderTarget[] | null;
}
export const ORDER_DONE = new Set(['finished', 'failed', 'timeout', 'cancelled', 'skipped']);

// ---- §5 spawn -------------------------------------------------------------------

export interface SpawnSlot {
  agent?: string;
  machineId?: string;
  profileId?: string;
  yolo?: boolean;
  /** undefined = the default of the role or agent in settings, '' = the CLI's own choice. */
  model?: string;
  effort?: string;
}
export interface SpawnRequest {
  slots?: SpawnSlot[];
  count?: number;
  agent?: string;
  group?: { name?: string; role?: Role; color?: GroupColor } | { id: string };
  orchestratorId?: string;
  at?: { x: number; y: number };
}
export interface SpawnResponse {
  sessions: Session[] | null;
  groupNodeId?: string;
}

// ---- §7 mission map -----------------------------------------------------------------

export interface MissionDirector {
  id: string;
  kind: 'bar' | 'orchestrator';
  title: string;
  lastOrder?: { text: string; at: string } | null;
  targets: string[];
}
export interface MissionGroup {
  nodeId: string;
  name: string;
  role: Role | string;
  color: GroupColor | string;
  members: string[];
  lastTask?: string;
}
export interface MissionConsole {
  sessionId: string;
  nodeId: string;
  handle: string;
  agent: string;
  role?: string;
  groupNodeId?: string;
  agentState: string;
  activity: string;
  stateSince: string;
  paused: boolean;
  subagents: number;
  currentOrder?: { orderId: string; text: string } | null;
  directedBy: string[];
}
export interface Mission {
  workspaceId: string;
  directors: MissionDirector[] | null;
  groups: MissionGroup[] | null;
  consoles: MissionConsole[] | null;
  counts: { working: number; needsYou: number; idle: number; done: number };
}

// ---- §9 handoff / §10 forward -----------------------------------------------------------

export type HandoffState = 'collecting' | 'summarizing' | 'spawning' | 'waitingReady' | 'delivering' | 'done' | 'failed' | 'manual';
export interface Handoff {
  id: string;
  sourceSessionId: string;
  targetSessionId: string | null;
  agent: string;
  state: HandoffState | string;
  jobId?: string;
  brief?: string;
  error?: string;
  createdAt: string;
  /** Switched this console instead of starting another. */
  inPlace?: boolean;
  /** The agent went back to its earlier conversation. */
  returning?: boolean;
}
export interface HandoffRequest {
  agent: string;
  machineId?: string;
  profileId?: string;
  workspaceId?: string;
  note?: string;
  threadId?: string;
  inPlace?: boolean;
  noBrief?: boolean;
  fresh?: boolean;
  model?: string;
  effort?: string;
}

export interface Forward {
  id: string;
  projectId: string;
  sources: string[];
  targets: string[];
  mode: ForwardMode;
  orderId?: string;
  state: 'waiting' | 'collecting' | 'sent' | 'expired' | 'cancelled' | string;
  createdAt: string;
  expiresAt: string;
}

// ---- §11 loops -------------------------------------------------------------------

export type LoopTrigger =
  | { kind: 'interval'; everySec: number }
  | { kind: 'cron'; expr: string }
  | { kind: 'agentIdle' }
  | { kind: 'output'; pattern: string; regex?: boolean };

export type LoopGoal =
  | { kind: 'agentJudged'; text: string; agent?: string }
  | { kind: 'command'; command: string; timeoutSec?: number };

export interface LoopTargets {
  kind: 'handles' | 'group' | 'orchestrator' | 'allAgents';
  ids: string[];
}

export interface LoopStop {
  maxRuns?: number;
  maxDurationSec?: number;
  until?: string;
  outputPattern?: string;
  goal?: LoopGoal;
}

export interface LoopState {
  status: 'running' | 'paused' | 'stopped' | 'done' | string;
  runs: number;
  startedAt?: string;
  lastRunAt?: string;
  nextRunAt?: string;
  stoppedReason?: 'maxRuns' | 'time' | 'goal' | 'output' | 'user' | 'error' | string;
}

export interface LoopDef {
  id: string;
  projectId: string;
  workspaceId: string;
  name: string;
  enabled: boolean;
  trigger: LoopTrigger;
  text: string;
  targets: LoopTargets;
  stop: LoopStop;
  createdBy?: 'user' | 'order' | string;
  createdAt?: string;
  state?: LoopState;
}

export type LoopInput = Omit<LoopDef, 'id' | 'state' | 'createdAt' | 'createdBy'>;

export interface LoopHistoryEntry {
  at: string;
  kind: 'run' | 'skip' | 'retry' | 'goalCheck' | 'stop' | string;
  note: string;
  targets?: { sessionId: string; result: 'delivered' | 'busy' | 'needsYou' | 'paused' | 'notAgent' | string }[] | null;
  goal?: { met: boolean; reason: string } | null;
}

export interface LoopTemplate {
  id?: string;
  name: string;
  description?: string;
  trigger?: LoopTrigger;
  text?: string;
  stop?: LoopStop;
  targets?: LoopTargets;
}

// ---- §12 memory ------------------------------------------------------------------------

export const FACT_KINDS = ['Architecture', 'Structure', 'Commands', 'Conventions', 'Gotchas', 'Context'] as const;
export interface MemoryDoc {
  content: string;
  mtime: string;
}
export interface MemoryBoth {
  project: MemoryDoc;
  global: MemoryDoc;
  /** Notes agents saved in the project's memory folder. */
  notes?: { name: string; content: string }[];
}
export interface Fact {
  kind: string;
  text: string;
}
export interface PushResult {
  delivered: number;
  busy: number;
  skipped: number;
  notAgent: number;
  /** Already had everything: no turn was used. */
  upToDate?: number;
  /** What was typed, over all consoles. */
  chars?: number;
  deliveryIds?: string[] | null;
}

/** Runner job (API-studio §1.2). */
export interface Job {
  id: string;
  kind: string;
  purpose?: string;
  projectId?: string;
  machineId?: string;
  agent?: string;
  state: 'queued' | 'running' | 'done' | 'failed' | 'cancelled' | string;
  progress?: { phase?: string; text?: string; pct?: number | null } | null;
  createdAt?: string;
  startedAt?: string | null;
  finishedAt?: string | null;
  elapsedMs?: number;
  error?: string;
  code?: string;
  result?: unknown;
}

// ---- §13 layouts --------------------------------------------------------------------------

export interface LayoutLane {
  key: string;
  agent?: string;
  role?: Role | string;
  group?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}
export interface LayoutGroup {
  key: string;
  name: string;
  role: Role | string;
  color: GroupColor | string;
}
export interface LayoutNode {
  key: string;
  kind: string;
  x: number;
  y: number;
  w: number;
  h: number;
  data?: Record<string, unknown>;
}
export interface Layout {
  id: string;
  name: string;
  /** One line on what a built-in is for. */
  description?: string;
  builtin: boolean;
  createdAt?: string;
  lanes: LayoutLane[] | null;
  groups: LayoutGroup[] | null;
  nodes: LayoutNode[] | null;
  wires: { from: string; to: string; kind: 'wire' | 'forward' | string }[] | null;
}
export interface LaneOverride {
  agent?: string;
  machineId?: string;
  profileId?: string;
  yolo?: boolean;
}
export interface DeployRequest {
  layoutId: string;
  at?: { x: number; y: number };
  fill?: { agent: string };
  laneOverrides?: Record<string, LaneOverride>;
  /** How many consoles a group has, by group key, where it differs from the template. */
  groupSizes?: Record<string, number>;
}

// ---- §14 events ---------------------------------------------------------------------------

export type OrchEvent =
  | { type: 'delivery'; delivery: Delivery }
  | { type: 'thread'; threadId: string; message: ThreadMessage }
  | { type: 'threadCleared'; threadId: string }
  | { type: 'order'; order: Order }
  | { type: 'confirmation'; confirmation: Confirmation }
  | { type: 'command'; commandId: string; clientId: string; state: 'planning' | 'done' | 'failed' | string; uiActions?: UiAction[] | null }
  | { type: 'handoff'; handoff: Handoff }
  | { type: 'forward'; forward: Forward }
  | { type: 'loop'; loop: LoopDef }
  | { type: 'loopRemoved'; id: string }
  | { type: 'loopRun'; loopId: string; entry: LoopHistoryEntry }
  | { type: 'memory'; projectId: string; scope: 'project' | 'global'; mtime: string }
  | { type: 'layouts' }
  | { type: 'job'; job: Job }
  | { type: 'jobOutput'; jobId: string; seq: number; text: string };
