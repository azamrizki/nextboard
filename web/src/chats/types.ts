// Chats (resume chat): every agent conversation that ran in a project, at the place it ran, and
// the lead's thread split into chats. The hub's shapes are in docs/API-agents.md (agent chats)
// and docs/API-orchestration.md (lead chats).

/** live: a console holds it and runs; asleep: held by a sleeping console; closed: no console;
 *  orphan: its part is gone or its folder moved, so it cannot resume where it started. */
export type ChatStatus = 'live' | 'asleep' | 'closed' | 'orphan';

export interface Chat {
  id: string;
  projectId: string;
  /** Catalog id (claude, codex, fakeclaude, …). */
  agent: string;
  agentSessionId?: string;
  profileId?: string;
  providerId?: string;
  /** Where it started: a part, or the project root (root), or neither (a console in no part). */
  partId?: string;
  root?: boolean;
  /** Where the CLI ran (This PC for a server part), and the server folder it worked on. */
  machineId: string;
  remoteMachineId?: string;
  remoteCwd?: string;
  /** The CLI's folder: a chat resumes only there. */
  cwd: string;
  title: string;
  messages: number;
  startedAt: string;
  lastAt: string;
  /** The console's handle when it last held the chat. */
  handle: string;
  workspaceId?: string;
  missionId?: string;
  transcriptPath?: string;
  archived?: boolean;
}

/** What the hub sends: the chat with its status and names as they are now. */
export interface ChatView extends Chat {
  status: ChatStatus;
  /** The console holding it (live or asleep). */
  sessionId?: string;
  /** "Root" or the part's name. */
  placeLabel?: string;
  machineName?: string;
  partName?: string;
  /** false: the agent keeps no session ids, so resuming continues its last conversation in that folder. */
  precise?: boolean;
  /** Why an orphan cannot resume where it started. */
  orphanReason?: 'part_removed' | 'folder_missing' | string;
}

/** What changed in its place since the chat's last message (told to the agent on resume). */
export interface ChatChanges {
  /** One line for the dialog ("6 new commits in web, 2 memory facts"). */
  summary?: string;
  /** The text the agent would get. */
  text?: string;
  /** New commits, per part (the root lists each part). */
  commits?: { partId?: string; part?: string; count?: number; subjects?: string[] | null }[] | null;
  /** Files changed since. */
  files?: number;
  /** Memory facts added since (main memory and the part's page). */
  facts?: string[] | null;
}

/** 409 handle_taken: who holds the chat's old handle, and the next free name. */
export interface HandleTaken {
  taken: string;
  /** The console holding it, and its handle (the same as taken). */
  by: string;
  byHandle?: string;
  /** The chat that console holds now, when it holds one. */
  byChat?: string;
  next: string;
  /** Whether the chat could be swapped into that console, and why not. */
  swappable?: boolean;
  reason?: string;
}

/** GET /api/chats/{id}: the view, its last reply and (with ?changes=1) what changed since. */
export interface ChatDetail extends ChatView {
  lastReply?: string;
  changes?: ChatChanges | null;
  /** Set when the chat's old handle is held by another console (the same as a 409 handle_taken). */
  handleTaken?: HandleTaken | null;
}

export interface ChatTranscriptMessage {
  id: string;
  role: 'user' | 'assistant' | 'tool' | string;
  at: string;
  text: string;
  tool?: string;
  sidechain?: boolean;
}

export interface ChatTranscript {
  agentSessionId?: string;
  messages: ChatTranscriptMessage[] | null;
  truncated?: boolean;
}

/** POST /api/chats/{id}/resume. */
export interface ResumeBody {
  workspaceId: string;
  /** Swap into this console (idle, same agent and folder) instead of starting a new one. */
  into?: string;
  /** The handle of the new console, when the old one is taken. */
  handle?: string;
  tellChanges?: boolean;
  /** Where the new console's tile goes (board coordinates). */
  x?: number;
  y?: number;
  /** From an editor card's agent pane: a console of that card, without a tile. */
  panel?: boolean;
  card?: string;
}

/** POST /api/chats/{id}/continue: summarise it and start fresh at another place. */
export interface ContinueBody {
  workspaceId: string;
  place: { partId?: string; root?: boolean };
  agent?: string;
}

/** A segment of the lead's thread (docs/API-orchestration.md). */
export interface LeadChat {
  id: string;
  threadId: string;
  title: string;
  createdAt: string;
  lastAt: string;
  messages: number;
  summary?: string;
  summaryUpTo?: string;
  missionIds?: string[] | null;
  /** The owner named it (a rename, or their first message); a new chat is "New chat" until then. */
  titled?: boolean;
}

export interface LeadChatList {
  active: string;
  items: LeadChat[] | null;
}

/** Events on /api/events. */
export type ChatEvent = { type: 'chat'; chat: ChatView } | { type: 'leadChats'; threadId: string; active: string };
