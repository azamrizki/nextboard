// Typed models mirroring docs/API.md and docs/API-board.md. Field names are the exact JSON (camelCase) names.

import type { ChatView } from '../chats/types';
import type { OrchEvent } from '../orch/types';
import type { StudioEvent } from '../studio/types';
import type { Beat, Monitor } from '../uptime/api';

/** Which build a hub runs (docs/API.md Hub); older hubs send only the version. */
export interface BuildInfo {
  version: string;
  /** Short hash, "-dirty" with uncommitted changes, "unknown" when not recorded. */
  commit?: string;
  /** Build time, RFC 3339; "" when not known. */
  builtAt?: string;
  channel?: 'stable' | 'beta' | 'dev' | string;
  /** GitHub owner/name of the releases and commits. */
  repo?: string;
}

export interface Health extends BuildInfo {
  ok: boolean;
  name: string;
}

export interface SetupStatus {
  needsSetup: boolean;
  hubName: string;
  /** This hub's instance ("default" for ~/.nextboard); never a path before login. */
  instance?: string;
  defaultInstance?: boolean;
  hubId?: string;
  /** While setup is needed: the hostname, with " · <instance>" for a named instance. */
  suggestedHubName?: string;
}

export interface Hub extends BuildInfo {
  name: string;
  urls: string[];
  listen: string;
  // ---- instances of This PC (docs/API.md Instances); paths and siblings for admins only ----
  instance?: { name: string; default: boolean };
  hubId?: string;
  dataDir?: string;
  configFile?: string;
  /** kind "none": no service of this instance runs this folder (started by hand, make dev). */
  service?: HubService;
  statusListen?: string;
  /** Another program is at the hub's path since it started (an update): it runs it after a restart. */
  replacedSinceStart?: boolean;
  binary?: { path: string; replacedSinceStart: boolean };
  siblings?: HubSibling[];
}

export interface HubService {
  kind: 'systemd' | 'launchd' | 'none' | string;
  name?: string;
  unitPath?: string;
  /** This process is the one the service runs. */
  managed?: boolean;
}

/** Another instance of This PC (GET /api/hub, admins). */
export interface HubSibling {
  name: string;
  hubName?: string;
  listen: string;
  port?: number;
  urls: string[];
  running: boolean;
  state: 'running' | 'stopped' | string;
  service?: string;
  problems?: string[];
}

/** An instance as GET /api/instances lists it. */
export interface InstanceInfo {
  name: string;
  hubId?: string;
  dataDir: string;
  configFile: string;
  configured: boolean;
  listen: string;
  statusListen?: string;
  previewRange: [number, number];
  service?: { kind: string; name: string; unitPath: string; bin: string };
  running: boolean;
  pid?: number;
  problems?: string[];
}

/** GET /api/instances (admin). */
export interface InstancesList {
  self: string;
  items: InstanceInfo[] | null;
  suggest: { host: string; loopback: boolean; hostname: string; port?: number; previewRange?: [number, number] };
  noFreeBlock?: string;
}

/** POST /api/instances (admin). */
export interface NewInstanceBody {
  name: string;
  hubName?: string;
  listenHost: string;
  port?: number;
  allowNetwork?: boolean;
  password: string;
}

/** 201 of POST /api/instances. */
export interface CreatedInstance {
  instance: string;
  hubId: string;
  dataDir: string;
  configFile: string;
  listen: string;
  previewRange: [number, number];
  service?: { kind: string; name: string; unitPath: string };
  started: boolean;
  healthy: boolean;
  /** How to start it, when no service manager could. */
  command?: string;
  urls: string[];
  /** Single use, until expiresAt. */
  setupUrls?: string[];
  expiresAt?: string;
  warnings?: string[];
  startError?: string;
}

/** GET /api/hub/update (docs/API.md Hub update). */
export interface HubUpdate {
  status: 'current' | 'available' | 'dev' | 'unknown';
  current: string;
  channel: string;
  latest?: string;
  url?: string;
  command?: string;
  error?: string;
  checkedAt: string;
  blocked?: string;
  blockedCode?: string;
  jobId?: string;
  installed?: string;
  restartError?: string;
  /** The instances whose service runs this binary, this one too (self): an update restarts them all. */
  restarts?: { name: string; listen: string; service: string; self?: boolean }[];
  /** The release the owner skipped for good (the update popup's "Skip this version"). */
  skipped?: string;
  /** "Skip for now": the popup stays away until then. */
  remindAfter?: string;
  /** Whether the popup asks now: a newer release is out, not skipped, not put off, not installing. */
  remind?: boolean;
}

/** The owner's answer to the update popup (POST /api/hub/update/remind). */
export interface UpdateReminder {
  skipped?: string;
  remindAfter?: string;
  remind: boolean;
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
  /** Set while archived (RFC 3339): not connected, not probed, absent from pickers (docs/API.md Machines). */
  archivedAt?: string;
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

export type SessionStatus = 'starting' | 'running' | 'exited' | 'reconnecting' | 'error' | 'asleep';

export interface Session extends SessionAgentFields {
  id: string;
  /** Agents this console ran before, newest first. */
  pastRuns?: PastRun[];
  /** A terminal or agent of the Code workbench (no board node). */
  panel?: boolean;
  /** Of a panel session: the editor card (node id) it was opened in, so that an orchestrator card wired to that card directs it. */
  card?: string;
  /** A login profile's sign-in console: no order, loop or task types into it. */
  signIn?: boolean;
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
  /**
   * The part of the project it works in (ROADMAP #49). From v0.9.0-beta.2 absent or "" is not
   * the first part: with root it is a root console, without it a console in no part's folder
   * (lib/parts.ts placementOfSession). Older hubs sent none for the first part's consoles.
   */
  partId?: string;
  /** At the project root, reaching every part (v0.9.0-beta.2): on This PC, in the hub-made root folder. */
  root?: boolean;
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
  /** Opens in this part of the project: its machine and folder. Found from them when absent. */
  partId?: string;
  /** Opens at the project root (v0.9.0-beta.2): not with partId, machineId other than "local" or cwd (400 bad_placement). */
  root?: boolean;
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
  /** With panel: the editor card (node id) the pane is opened in. */
  card?: string;
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

/** One folder of a project, on one machine, named for its role (docs/API-board.md §1, ROADMAP #49). */
export interface ProjectPart {
  id: string;
  name: string;
  machineId: string;
  path: string;
  /** Agents only read there. */
  readOnly?: boolean;
  /** Commands that need the owner's approval there, on top of the hub's and the project's. */
  blocked?: string[];
}

/** GET /api/projects/{id}/merge?from=: what merging another project into this one would do. */
export interface MergePreview {
  from: string;
  parts: ProjectPart[];
  workspaces: { id: string; name: string }[];
  sessions: number;
  running: number;
  monitors: number;
  loops: number;
  memoryChars: number;
  subMemories: number;
  criticalChars: number;
  criticalFits: boolean;
  publicPage: boolean;
  problem?: string;
  code?: string;
}

export interface Project {
  id: string;
  name: string;
  /** Those of the first part. */
  machineId: string;
  path: string;
  /** The folders it spans, the first being machineId and path. A hub from before parts sends none. */
  parts?: ProjectPart[];
  /** What agents may do in the parts other than their own: absent = read, "full" = everything. */
  partAccess?: 'full';
  /** Ids of projects merged into this one: their old links still find it. */
  aliases?: string[];
  /** Two or more parts: the project's root folder on This PC, where root consoles work (docs/API-board.md §1). */
  rootDir?: string;
  createdAt: string;
  lastOpenedAt: string;
  /** In tab order. */
  workspaces: Workspace[] | null;
  sessionCount: number;
  runningCount: number;
  /** Absent: under All projects. */
  groupId?: string;
  tags?: string[];
  /** The one badge it carries (ProjectBadge.id); absent when none. */
  badgeId?: string;
  /** What its agents' commands may do (docs/API-agents.md §27); absent = what each CLI does. */
  sandbox?: 'workspace' | 'offline';
  /** Set while archived (RFC 3339): its consoles are stopped, everything else is kept (API-board §1). */
  archivedAt?: string;
  /** The poster across the top of its card; absent when none. */
  cover?: ProjectImage;
  /** The image next to its name; absent when none (the monogram shows). */
  icon?: ProjectImage;
}

/** A project's cover or icon: an image in the asset store (API-board §1, §5). */
export interface ProjectImage {
  assetId: string;
  source: 'upload' | 'ai';
  at: string;
  /** A cover made with AI as a background. The card now sets the project's name over every cover (coverTitle). */
  title?: boolean;
  /** The short line under the name. */
  tagline?: string;
  /** A colour Create with AI picked, #RRGGBB. Kept, but no longer shown: every cover title is white. */
  accent?: string;
}

/** What a bulk request does to each id (API-board §1, docs/API.md Machines). `delete` is permanent. */
export type BulkAction = 'archive' | 'restore' | 'delete';

/** One id of a bulk request: each is handled on its own. */
export interface BulkResult {
  id: string;
  ok: boolean;
  /** Why not, in words ("in use by acme-shop"). */
  error?: string;
  /** The same as a code: in_use, not_archived, project_not_found, local, … */
  code?: string;
}

export interface BulkResponse {
  results: BulkResult[] | null;
}

/** GET /api/art/capability: whether "Create with AI" can make an image on This PC. */
export interface ArtCapability {
  available: boolean;
  tool: string;
  reason?: string;
}

export type ArtKind = 'cover' | 'icon';

/** 409 in_use: the projects whose parts are on a machine (docs/API.md Machines). */
export interface MachineUser {
  id: string;
  name: string;
}

/** GET /api/agents/sandbox: whether each CLI can build its sandbox on This PC. */
export interface SandboxStatus {
  claude: { ready: boolean; reason?: string };
  codex: { ready: boolean; reason?: string };
}

/** One level of the tree over the projects. No parent: the top level. */
export interface ProjectGroup {
  id: string;
  name: string;
  parentId?: string;
  createdAt: string;
}

/** The colours a badge can have (API-board, project badges). */
export type BadgeColor = 'red' | 'orange' | 'amber' | 'green' | 'teal' | 'blue' | 'violet' | 'pink' | 'gray';

/** A short coloured label with an icon, shared by the projects that carry it: one per project. */
export interface ProjectBadge {
  id: string;
  /** At most 12 characters; shown in capitals. */
  name: string;
  color: BadgeColor;
  /** A name of the web's icon set; absent for none. */
  icon?: string;
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
  /** The board's project; absent in archives from before it was kept. */
  projectId?: string;
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

export type AgentState = 'starting' | 'working' | 'needsYou' | 'ready' | 'shell' | 'exited' | 'reconnecting' | 'error' | 'asleep';

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
  /** ROADMAP #50 (API-agents §28): its process was ended to free memory; it resumes when opened,
   *  typed into or sent work. keepAwake: never put to sleep. unsentDraft: what was typed before
   *  it slept and could not be typed back. */
  asleep?: boolean;
  asleepAt?: string | null;
  keepAwake?: boolean;
  unsentDraft?: string;
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
    /** Idle minutes before an agent with its terminal closed goes to sleep (0 = never). */
    sleepAfterMin?: number;
    /** After a hub restart, agents stay asleep until opened or given work. */
    lazyStart?: boolean;
    /** The agents of this many projects opened last stay awake when idle and start after a restart. */
    keepRecentProjects?: number;
    /** A resumed chat is told what changed in its place since its last message (default on). */
    resumeTellChanges?: boolean;
  };
  notifications: { needsYou: boolean; finished: boolean; error: boolean; finishedMinSec: number };
  shortcuts: ShortcutMap;
  /** Phase 7 (docs/API-studio.md §12). */
  previews?: { auto: boolean; portRange: [number, number] };
  /** Code workbench (docs/API-studio.md §7.2). */
  editor?: { onSave: { pattern: string; command: string }[] | null };
  /** The lead agent (docs/API-orchestration.md §3.9). */
  lead?: {
    name: string;
    personality: string;
    talkFirst: boolean;
    /** "" = like hidden runs (Claude Code), "claude" | "codex" on its login, or an API provider id. */
    runner?: string;
    /** The lead's model and effort on that runner; absent = its default. */
    model?: string;
    effort?: string;
    /** agent id → API provider id for the consoles the lead starts. */
    providers?: Record<string, string>;
    /** The language the lead writes to the owner in: "" follows the owner's messages, "id", "en". */
    language?: string;
    /** A mission's limits before the lead asks whether to go on (30 rounds, 4 hours). */
    missionRounds?: number;
    missionHours?: number;
  };
  usage?: { background: boolean; agents?: Record<string, boolean>; chime: { enabled: boolean; play: 'after100' | 'always'; sound: 'bell' | 'chime' | 'pop' | 'soft' } };
  runner?: { defaultAgent: 'claude' | 'codex' | string; /** hidden runs through this API provider; absent = the login */ provider?: string };
  git?: { commitWithRunner: boolean };
  /** Choices of the web app every browser follows (docs/API-agents.md §11). */
  ui?: { openProjectsIn: OpenProjectsIn };
  [key: string]: unknown;
}

/** Where opening a project goes: "" asks each time, else that view. */
export type OpenProjectsIn = '' | 'board' | 'code';

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
      projectBadges?: ProjectBadge[] | null;
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
  | { type: 'projectBadges'; badges: ProjectBadge[] | null }
  /** After a bulk request: fetch the lists again. */
  | { type: 'projects' }
  | { type: 'machines' }
  | { type: 'boardOps'; workspaceId: string; rev: number; clientId: string; ops: Op[] | null }
  | { type: 'attention'; attention: Attention }
  | { type: 'uptimeBeat'; projectId: string; monitorId: string; beat: Beat; monitor: Monitor }
  | { type: 'uptimeMonitor'; monitor: Monitor }
  | { type: 'uptimeRemoved'; projectId: string; id: string }
  /** A project's uptime settings changed: fetch them again (they hold the webhook, so the event does not). */
  | { type: 'uptimeSettings'; projectId: string }
  | { type: 'vaultChanged'; rev: number; vaultId: string }
  | { type: 'credentialRequest'; id: string; state: string }
  | { type: 'passkeyRequest'; id?: string }
  | { type: 'settings'; settings: Settings }
  | { type: 'agents' }
  | { type: 'profile'; profile: Profile }
  | { type: 'profileRemoved'; id: string }
  /** An agent chat was recorded or changed (chats/store.ts). */
  | { type: 'chat'; chat: ChatView }
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
