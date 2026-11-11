// Typed models mirroring docs/API.md and docs/API-board.md. Field names are the exact JSON (camelCase) names.

import type { OrchEvent } from '../orch/types';
import type { StudioEvent } from '../studio/types';
import type { Beat, Monitor } from '../uptime/api';

export interface Health {
  ok: boolean;
  name: string;
  version: string;
}

export interface SetupStatus {
  needsSetup: boolean;
  hubName: string;
}

export interface Hub {
  name: string;
  version: string;
  urls: string[];
  listen: string;
}

export type Me =
  | { kind: 'admin' }
  | { kind: 'device'; deviceId: string; deviceName: string };

export interface PairStart {
  code: string;
  expiresAt: string;
  uri: string;
  urls: string[];
  hubName: string;
}

export interface Device {
  id: string;
  name: string;
  platform: string;
  createdAt: string;
  lastSeen: string;
  online: boolean;
}

export type MachineKind = 'local' | 'ssh';
export type AuthMethod = 'sshconfig' | 'agent' | 'key' | 'password';
export type PersistMode = 'auto' | 'tmux' | 'direct';
export type MachineStatus =
  | 'local'
  | 'connecting'
  | 'connected'
  | 'disconnected'
  | 'reconnecting'
  | 'hostkey'
  | 'auth'
  | 'error';

/** What a machine waits for that only a person can give (docs/API.md). */
export interface MachineAuthNeed {
  kind: 'password' | 'passphrase';
  /** The key a passphrase is for; missing: the key uploaded for this machine. */
  keyPath?: string;
  host: string;
  prompt?: string;
  wrong?: boolean;
  /** Asked for a passphrase, but the server would also take a password. */
  canPassword?: boolean;
}

export interface PendingHostKey {
  fingerprint: string;
  keyType: string;
  host: string;
}

export interface Probe {
  os: string;
  arch: string;
  shell: string;
  home: string;
  tmux: boolean;
  /** command name → resolved path ("" = not found) */
  commands: Record<string, string>;
  probedAt: string;
}

/** An MCP server of NextBoard (docs/API-agents.md §23). Values are write-only. */
export interface McpVar {
  name: string;
  /** Sent to set a value; empty keeps what is stored. Never comes back. */
  value?: string;
  set?: boolean;
}

export interface McpServer {
  id: string;
  description?: string;
  transport: 'http' | 'stdio';
  url?: string;
  headers: McpVar[];
  command?: string;
  args: string[];
  env: McpVar[];
  projects: string[];
  onServers: boolean;
  enabled: boolean;
}

export interface McpProbe {
  ok: boolean;
  tools: string[];
  needsLogin?: boolean;
  error?: string;
  tookMs: number;
}

/** A conversation a console left when it switched agents. */
export interface PastRun {
  agent: string;
  agentSessionId?: string;
  profileId?: string;
  /** An API provider instead of a login (docs/API-agents.md §10.2). */
  providerId?: string;
  at: string;
}

/** One line of the connection log (docs/API.md). */
export interface ConnEvent {
  at: string;
  kind: string;
  machineId?: string;
  machine?: string;
  sessionId?: string;
  reason?: string;
  error?: string;
  tool?: string;
  attempt?: number;
  misses?: number;
  durationMs?: number;
  uptimeMs?: number;
  downtimeMs?: number;
  rttAvgMs?: number;
  rttMaxMs?: number;
  calls?: number;
  failures?: number;
  version?: string;
  pid?: number;
  lastSeen?: string;
}

export interface ConnSummary {
  connects: number;
  connectFailed: number;
  disconnects: number;
  maxDowntimeMs: number;
  avgDowntimeMs: number;
  avgUptimeMs: number;
  keepaliveMiss: number;
  rttAvgMs: number;
  rttMaxMs: number;
  toolFailed: number;
  toolSlow: number;
  channelsLost: number;
  uncleanExits: number;
}

export interface ConnLogView {
  items: ConnEvent[];
  summary: ConnSummary;
}

export interface Machine {
  id: string;
  name: string;
  kind: MachineKind;
  host: string;
  port: number;
  user: string;
  sshConfigHost: string;
  authMethod: AuthMethod | '';
  persistMode: PersistMode;
  effectivePersist: 'tmux' | 'direct' | '';
  status: MachineStatus;
  error: string;
  pendingHostKey: PendingHostKey | null;
  hostKeyMismatch: boolean;
  needsAuth?: MachineAuthNeed | null;
  probe: Probe | null;
  sessionCount: number;
}

export interface SSHConfigHost {
  host: string;
  hostName: string;
  user: string;
  port: number;
  identityFile: string;
  proxyJump: string;
}

export interface Preset {
  id: string;
  name: string;
  command: string;
  args: string[] | null;
  resumeArgs: string[] | null;
  color: string;
  /** Only present on GET /api/machines/{id}/presets. */
  available?: boolean;
}

export interface DirEntry {
  name: string;
  dir: boolean;
  /** Bytes (files); present since Phase 4. */
  size?: number;
  mtime?: string;
}

export interface DirListing {
  path: string;
  parent: string;
  entries: DirEntry[] | null;
}

export interface AddMachine {
  name?: string;
  sshConfigHost?: string;
  host?: string;
  port?: number;
  user?: string;
  authMethod: AuthMethod;
  privateKey?: string;
  passphrase?: string;
  password?: string;
  persistMode?: PersistMode;
}

export type SessionStatus = 'starting' | 'running' | 'exited' | 'reconnecting' | 'error';

export interface Session extends SessionAgentFields {
  id: string;
  /** Agents this console ran before, newest first. */
  pastRuns?: PastRun[];
  /** A terminal or agent of the Code workbench (no board node). */
  panel?: boolean;
  name: string;
  preset: string;
  machineId: string;
  command: string;
  args: string[] | null;
  resumeArgs: string[] | null;
  env: string[] | null;
  cwd: string;
  cols: number;
  rows: number;
  tmuxName: string;
  createdAt: string;
  status: SessionStatus;
  exitCode: number;
  busy: boolean;
  startedAt: string;
  lastOutputAt: string;
  error: string;
  color: string;
  machineName: string;
  /** The agent runs on This PC and works on this server, in this folder. */
  remoteMachineId?: string;
  remoteCwd?: string;
  /** Phase 4: the project / workspace (board tab) the session belongs to. */
  projectId?: string;
  workspaceId?: string;
}

export interface NodeLayout {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface CreateSession {
  /** Board tab the terminal node lands on (docs/API-board.md §1). */
  workspaceId?: string;
  /** Defaults to the project's machine. */
  machineId?: string;
  preset: string;
  /** Phase 5: catalog id (alias of preset). */
  agent?: string;
  handle?: string;
  /** Default settings.agents.yoloDefault; ignored for agents without yolo. */
  yolo?: boolean;
  profileId?: string;
  /** An API provider instead of a login (docs/API-agents.md §10.2). */
  providerId?: string;
  /** undefined = the default in settings, '' = the CLI's own choice. */
  model?: string;
  effort?: string;
  launch?: { mode: LaunchMode; agentSessionId?: string };
  name?: string;
  /** Defaults to the project's path. */
  cwd?: string;
  cols?: number;
  rows?: number;
  /** Where the hub places the terminal node (board coordinates). */
  node?: NodeLayout;
  /** No board node: a terminal or agent of the Code workbench. */
  panel?: boolean;
}

export interface Viewport {
  x: number;
  y: number;
  zoom: number;
}

// ---- Phase 4: projects & workspaces (docs/API-board.md §1) ----------------------

export interface Workspace {
  id: string;
  projectId: string;
  name: string;
  createdAt: string;
  sessionCount: number;
}

/** What closing every board of a project would end. */
export interface CloseAllPreview {
  boards: number;
  tiles: number;
  sessions: number;
  running: number;
  apps: number;
  loops: number;
}

export interface CloseAllResult {
  /** The new empty board that remains. */
  workspace: Workspace;
  closed: number;
  archived: number;
  sessions: number;
  apps: number;
  loops: number;
}

export interface Project {
  id: string;
  name: string;
  machineId: string;
  path: string;
  createdAt: string;
  lastOpenedAt: string;
  /** In tab order. */
  workspaces: Workspace[] | null;
  sessionCount: number;
  runningCount: number;
  /** Absent: under All projects. */
  groupId?: string;
  tags?: string[];
}

/** One level of the tree over the projects. No parent: the top level. */
export interface ProjectGroup {
  id: string;
  name: string;
  parentId?: string;
  createdAt: string;
}

export interface GitInfo {
  isRepo: boolean;
  branch: string;
  ahead: number;
  behind: number;
  changed: number;
  error: string;
}

// ---- Phase 4: board v2 (docs/API-board.md §2) ------------------------------------

export interface BoardNode {
  id: string;
  kind: string;
  x: number;
  y: number;
  w: number;
  h: number;
  rotation?: number;
  locked?: boolean;
  data: Record<string, unknown>;
}

export interface BoardEdge {
  id: string;
  source: string;
  target: string;
  kind: string;
  data: Record<string, unknown>;
}

export interface Board {
  workspaceId: string;
  schemaVersion: number;
  rev: number;
  /** Array order = z order (last = top). */
  nodes: BoardNode[] | null;
  edges: BoardEdge[] | null;
}

export interface NodePatch {
  x?: number;
  y?: number;
  w?: number;
  h?: number;
  rotation?: number;
  locked?: boolean;
  /** Shallow-merged key by key. */
  data?: Record<string, unknown>;
}

export type Op =
  | { op: 'put'; node: BoardNode }
  | { op: 'patch'; id: string; set: NodePatch }
  | { op: 'del'; id: string }
  | { op: 'z'; id: string; to: 'front' | 'back' }
  | { op: 'putEdge'; edge: BoardEdge }
  | { op: 'delEdge'; id: string };

export interface OpsRequest {
  clientId: string;
  baseRev: number;
  ops: Op[];
}

export interface SnapshotInfo {
  id: string;
  at: string;
  nodeCount: number;
}

export interface ArchiveEntry {
  id: string;
  projectName: string;
  workspaceName: string;
  archivedAt: string;
  nodeCount: number;
  reason: 'closed' | 'cleared' | 'unreadable' | string;
}

// ---- Phase 4: machine files & assets (§4, §5) -----------------------------------

export interface FsStat {
  path: string;
  name: string;
  dir: boolean;
  size: number;
  mtime: string;
  exists: boolean;
}

export interface FsRead {
  path: string;
  size: number;
  mtime: string;
  binary: boolean;
  truncated: boolean;
  content: string;
}

export interface FsWrite {
  path: string;
  content: string;
  encoding: 'utf8' | 'base64';
  ifMtime?: string;
  unique?: boolean;
}

export interface Asset {
  id: string;
  url: string;
  type: string;
  size: number;
  w: number;
  h: number;
}

// ---- Phase 5: agents, live status, notifications (docs/API-agents.md) -------------

export type AgentKind = 'agent' | 'shell';
export type AgentSource = 'builtin' | 'catalog' | 'custom';
export type ResumeKind = 'session-id' | 'continue' | 'picker' | 'none';
export type LaunchMode = 'fresh' | 'resume' | 'continue' | 'pick' | 'shell';

export interface AgentResume {
  kind: ResumeKind;
  launchArgs: string[] | null;
  resumeArgs: string[] | null;
  continueArgs: string[] | null;
  pickerArgs: string[] | null;
}

/** Catalog entry (= Preset v2, §1). */
export interface Agent {
  id: string;
  /** A launch can name a model, an effort. */
  models?: boolean;
  efforts?: boolean;
  name: string;
  kind: AgentKind;
  source: AgentSource;
  /** Custom only: built-in whose capabilities it inherits ("claude" | "codex" | ""). */
  base: string;
  command: string;
  args: string[] | null;
  env: string[] | null;
  color: string;
  detect: string;
  yoloArgs: string[] | null;
  yoloEnv: string[] | null;
  resume: AgentResume;
  liveStatus: 'claude-hooks' | 'codex-notify' | 'screen' | 'none' | string;
  transcript: 'claude' | 'codex' | 'none' | string;
  profileEnv: string;
  screenRules: string;
  /** Legacy = resume.continueArgs. */
  resumeArgs: string[] | null;
  /** Only on GET /api/machines/{id}/agents. */
  available?: boolean;
  /** Turned off in Settings › Agents (settings.agents.disabled). */
  disabled?: boolean;
}

export interface MachineAgents {
  defaultAgent: string;
  agents: Agent[] | null;
}

/** POST/PATCH /api/agents body (custom agents). */
export interface AgentInput {
  id?: string;
  name: string;
  command: string;
  args?: string[];
  env?: string[];
  color?: string;
  kind?: AgentKind;
  base?: string;
  yoloArgs?: string[];
  yoloEnv?: string[];
  resume?: Partial<AgentResume>;
}

export type AgentState = 'starting' | 'working' | 'needsYou' | 'ready' | 'shell' | 'exited' | 'reconnecting' | 'error';

/** Session fields added by Phase 5 (§7). Optional: Phase-4 hubs don't send them. */
/** A model and an effort ('' = what the CLI does when told nothing). */
export interface ModelChoice {
  model?: string;
  effort?: string;
}

/** One row of what a CLI says it can run (docs/API-agents.md §25). */
export interface AgentModel {
  id: string;
  name: string;
  description?: string;
  efforts?: string[];
  defaultEffort?: string;
}

export interface ModelList {
  models: AgentModel[];
  efforts: string[];
  current?: string;
  currentEffort?: string;
  source: string;
}

export interface SessionAgentFields {
  handle?: string;
  /** What this console runs, kept over restarts. */
  model?: string;
  effort?: string;
  agent?: string;
  agentKind?: AgentKind;
  agentState?: AgentState;
  stateSource?: string;
  stateSince?: string;
  activity?: string;
  needsYouSince?: string | null;
  foreground?: 'agent' | 'shell' | 'unknown';
  /** What runs in the foreground of a shell console; empty at its prompt. */
  job?: string;
  /** Really processing: an agent in a turn, or a shell with a command running. */
  active?: boolean;
  manualAgent?: boolean;
  liveStatus?: 'hooks' | 'notify' | 'pending' | 'screen' | 'off' | string;
  agentSessionId?: string;
  agentExitCode?: number | null;
  yolo?: boolean;
  profileId?: string;
  /** An API provider instead of a login (docs/API-agents.md §10.2). */
  providerId?: string;
  resumeOffer?: boolean;
  lastTurn?: { startedAt: string; endedAt: string; durationMs: number } | null;
  reply?: { at: string; chars: number; paragraphs: number } | null;
  /** Phase 6 (API-orchestration §8, §2): paused by the hub (SIGSTOP), active deliveries. */
  paused?: boolean;
  pausedAt?: string | null;
  queue?: number;
}

export interface Reply {
  sessionId: string;
  agentSessionId: string;
  source: 'transcript' | 'screen';
  text: string;
  at: string;
  chars: number;
  truncated: boolean;
  messageId?: string;
}

export interface Conversation {
  agentSessionId: string;
  title: string;
  startedAt: string;
  updatedAt: string;
  messages: number;
  sizeBytes: number;
  path: string;
  attachedTo: string | null;
  /** Handle of the console holding the conversation (Phase 5 addition). */
  attachedHandle?: string | null;
}

export interface ConversationList {
  items: Conversation[] | null;
  scanned: number;
  truncated: boolean;
}

export interface Profile {
  id: string;
  machineId: string;
  agent: string;
  name: string;
  dir: string;
  createdByHub: boolean;
  createdAt: string;
  exists: boolean;
  /** The agent's own state folder in the NextBoard data folder. */
  home?: boolean;
}

export interface ShortcutMap {
  select: string;
  hand: string;
  spawn: string;
  pen: string;
  eraser: string;
  git: string;
  memory: string;
  skills: string;
  loops: string;
  markdown: string;
  needsYou: string;
}

export interface Settings {
  schemaVersion: number;
  agents: {
    defaults: Record<string, string> | null;
    paths: Record<string, Record<string, string>> | null;
    profiles: Record<string, Record<string, string>> | null;
    disabled?: Record<string, boolean> | null;
    roleAgents?: Record<string, string> | null;
    /** What a new console starts with, per agent and per group role. */
    models?: Record<string, ModelChoice> | null;
    roleModels?: Record<string, ModelChoice> | null;
    yoloDefault: boolean;
    /** Claude Code may work on servers through hub tools. */
    remoteTools?: boolean;
    /** Agents may sign their commits and pull requests. */
    coAuthor?: boolean;
    liveStatus: boolean;
    /** Claude consoles use the editor card as their IDE. */
    ideLink: boolean;
    hookChannel: 'auto' | 'tty' | 'http';
    resumeOnRestart: 'auto' | 'ask' | 'never';
  };
  notifications: { needsYou: boolean; finished: boolean; error: boolean; finishedMinSec: number };
  shortcuts: ShortcutMap;
  /** Phase 7 (docs/API-studio.md §12). */
  previews?: { auto: boolean; portRange: [number, number] };
  /** Code workbench (docs/API-studio.md §7.2). */
  editor?: { onSave: { pattern: string; command: string }[] | null };
  /** The lead agent (docs/API-orchestration.md §3.9). */
  lead?: { name: string; personality: string; talkFirst: boolean };
  usage?: { background: boolean; agents?: Record<string, boolean>; chime: { enabled: boolean; play: 'after100' | 'always'; sound: 'bell' | 'chime' | 'pop' | 'soft' } };
  runner?: { defaultAgent: 'claude' | 'codex' | string; /** hidden runs through this API provider; absent = the login */ provider?: string };
  git?: { commitWithRunner: boolean };
  [key: string]: unknown;
}

export type AttentionKind = 'needsYou' | 'finished' | 'error' | string;

export interface Attention {
  id: string;
  seq: number;
  kind: AttentionKind;
  sessionId: string;
  /** An uptime alert: about a monitor, not a console. */
  monitorId?: string;
  handle: string;
  agent: string;
  machineName: string;
  projectId: string;
  workspaceId: string;
  title: string;
  body: string;
  at: string;
}

export interface AttentionList {
  bootId: string;
  seq: number;
  items: Attention[] | null;
  needsYou: { sessionId: string; handle: string; since: string }[] | null;
}

/** Frames received on /api/events. */
export type HubEvent =
  | {
      type: 'snapshot';
      sessions: Session[] | null;
      machines: Machine[] | null;
      devices: Device[] | null;
      projects?: Project[] | null;
      projectGroups?: ProjectGroup[] | null;
    }
  | { type: 'session'; session: Session }
  | { type: 'removed'; id: string }
  | { type: 'machine'; machine: Machine }
  | { type: 'machineRemoved'; id: string }
  | { type: 'device'; device: Device }
  | { type: 'deviceRemoved'; id: string }
  | { type: 'project'; project: Project }
  | { type: 'projectRemoved'; id: string }
  | { type: 'projectGroups'; groups: ProjectGroup[] | null }
  | { type: 'boardOps'; workspaceId: string; rev: number; clientId: string; ops: Op[] | null }
  | { type: 'attention'; attention: Attention }
  | { type: 'uptimeBeat'; projectId: string; monitorId: string; beat: Beat; monitor: Monitor }
  | { type: 'uptimeMonitor'; monitor: Monitor }
  | { type: 'uptimeRemoved'; projectId: string; id: string }
  | { type: 'vaultChanged'; rev: number; vaultId: string }
  | { type: 'credentialRequest'; id: string; state: string }
  | { type: 'passkeyRequest'; id?: string }
  | { type: 'settings'; settings: Settings }
  | { type: 'agents' }
  | { type: 'profile'; profile: Profile }
  | { type: 'profileRemoved'; id: string }
  | OrchEvent
  | StudioEvent;

/** Text frames received on /api/sessions/{id}/ws. */
export interface SessionStatusFrame {
  t: 'status';
  status: SessionStatus;
  exitCode: number;
  busy: boolean;
  /** Phase 5 (§7). */
  agentState?: AgentState;
  activity?: string;
  needsYouSince?: string | null;
  foreground?: 'agent' | 'shell' | 'unknown';
  job?: string;
  active?: boolean;
}
