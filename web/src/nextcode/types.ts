// NextCode's chat (docs/API-agents.md §30): the shapes the hub sends and takes. The source of truth
// is internal/nextcode/types.go; keep the two in step.

export type ItemKind = 'user' | 'text' | 'thinking' | 'tool' | 'prompt' | 'error' | 'notice' | 'answer';
export type ToolStatus = 'running' | 'done' | 'error' | 'denied' | 'stopped';

/** A tool call of the agent. */
export interface Tool {
  name: string;
  /** The tool's input as the CLI gave it (≤ 64 KB; `clipped` when cut). */
  input?: unknown;
  /** Its result as text (≤ 32 KB, head and tail). */
  output?: string;
  status: ToolStatus;
  clipped?: boolean;
}

/** One entry of the chat. */
export interface Item {
  id: string;
  kind: ItemKind;
  at: string;
  /** Markdown for text; thinking may be empty (the CLI hides it). */
  text?: string;
  tool?: Tool;
  /** The tool call of the sub-agent this belongs to; empty in the main conversation. */
  parent?: string;
  /** The id it had while streamed: the block it replaces. */
  stream?: string;
  /** Of an answer item: what was asked and answered. */
  answer?: Answered;
}

/** The owner's answer to the agent's questions or plan, as the chat keeps it (an answer item). */
export interface Answered {
  /** The request answered: a question (also when missing, in chats kept before plans were) or a plan. */
  kind?: 'question' | 'plan';
  /** The question's or plan's tool call (AskUserQuestion, ExitPlanMode), when the CLI shows one; Codex shows none. */
  toolUseId?: string;
  /** Of a question: the questions with their options, in AskUserQuestion's shape. A plan's is in its tool call. */
  input?: unknown;
  /** The chosen labels by question text; a label that is not an option is the owner's own words. */
  answers?: Record<string, string[]>;
  /** The owner's own reply; of a plan sent back, what to change in it. */
  message?: string;
  /** Nothing was answered: the agent went on without it. */
  skipped?: boolean;
  /** Of a plan: approved; else the agent keeps planning. */
  approved?: boolean;
  /** Of a plan approved: the permission mode it was approved into. */
  mode?: PermissionMode | string;
}

export type RequestKind = 'permission' | 'question' | 'plan';

/** Something the agent waits on the owner for. */
export interface Request {
  id: string;
  kind: RequestKind;
  tool: string;
  toolUseId?: string;
  input?: unknown;
  title?: string;
  displayName?: string;
  description?: string;
  blockedPath?: string;
  reason?: string;
  /** The CLI offers rules for next time: decision allowAlways applies them. */
  canRemember?: boolean;
  parent?: string;
  at: string;
}

export type Decision = 'allow' | 'allowAlways' | 'deny' | 'denyStop';

/** The owner's answer to a Request. */
export interface Answer {
  decision: Decision;
  /** Why (deny), or a free reply to a question. */
  message?: string;
  /** Of a question: the chosen labels by question text. */
  answers?: Record<string, string[]>;
  /** Of a plan approved: the permission mode it is carried out in. */
  mode?: PermissionMode;
}

export type PermissionMode = 'default' | 'acceptEdits' | 'plan' | 'auto' | 'bypassPermissions';
export type ProcessState = 'stopped' | 'starting' | 'running';

export interface Usage {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

/** The console's live state. */
export interface Status {
  engine: string;
  process: ProcessState;
  working: boolean;
  turnSince?: string;
  stopping?: boolean;
  model?: string;
  permissionMode?: PermissionMode | string;
  conversation?: string;
  /** Slash command names, without "/". */
  commands?: string[];
  costUsd?: number;
  usage?: Usage;
  /** The CLI's latest rate limit report, as it gives it. */
  rateLimit?: unknown;
  /** A passing state worth showing ("Retrying (attempt 2 of 10)…"). */
  note?: string;
  /** Why the last CLI ended by itself, until the next one starts. */
  error?: string;
}

export type BlockKind = 'text' | 'thinking' | 'tool';

/** One frame of GET …/nextcode/ws. */
export type Frame =
  | { t: 'snapshot'; items?: Item[] | null; more?: boolean; requests?: Request[] | null; status?: Status | null }
  | { t: 'item'; item: Item }
  | { t: 'block'; id: string; kind: BlockKind; name?: string; parent?: string }
  | { t: 'delta'; id: string; kind: BlockKind; text: string }
  | { t: 'request'; request: Request }
  | { t: 'resolved'; id: string }
  | { t: 'status'; status: Status };

/** GET /api/sessions/{id}/nextcode. */
export interface ChatPage {
  items: Item[] | null;
  more: boolean;
  requests: Request[] | null;
  status: Status;
}
