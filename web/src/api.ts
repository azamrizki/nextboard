// REST + WebSocket client for the NextBoard hub (contract: docs/API.md, docs/API-board.md).
//
// - Same-origin fetch; never reads cookies, so it also works behind the desktop
//   app's local proxy that injects an `Authorization: Bearer` header.
// - Every mutating request carries `X-NextBoard: 1` (CSRF guard).
// - Non-2xx responses throw an ApiError carrying the status and the `error` message.

import { limiter } from './lib/limiter';
import { packUpload, type Packed } from './lib/pack';
import { ReconnectingSocket, type ReconnectingSocketOptions } from './lib/socket';
import type { Job } from './orch/types';
import type {
  AddMachine,
  Agent,
  AgentInput,
  AttentionList,
  CloseAllPreview,
  CloseAllResult,
  ConversationList,
  LaunchMode,
  MachineAgents,
  Profile,
  Reply,
  Settings,
  ArchiveEntry,
  ArtCapability,
  ArtKind,
  Asset,
  BulkAction,
  BulkResponse,
  BulkResult,
  MachineUser,
  Board,
  ConnLogView,
  CreateSession,
  Device,
  DirListing,
  FsRead,
  FsStat,
  FsWrite,
  GitInfo,
  Health,
  Hub,
  HubUpdate,
  UpdateReminder,
  CreatedInstance,
  InstancesList,
  NewInstanceBody,
  Machine,
  McpProbe,
  McpServer,
  Me,
  OpsRequest,
  PairStart,
  PersistMode,
  Preset,
  MergePreview,
  ModelList,
  Project,
  ProjectBadge,
  ProjectGroup,
  SandboxStatus,
  Session,
  SetupStatus,
  SnapshotInfo,
  SSHConfigHost,
  Workspace,
} from './lib/types';

export * from './lib/types';
export { ReconnectingSocket, wsUrl, type SocketState } from './lib/socket';

export class ApiError extends Error {
  readonly status: number;
  /** Stable snake_case error code (Phase 5 hubs), "" when absent. */
  readonly code: string;
  /** Parsed JSON error body (e.g. 428 {"machine","paths"}, 409 {"paths"}). */
  readonly body: Record<string, unknown>;
  constructor(status: number, message: string, code = '', body: Record<string, unknown> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

export function isCode(err: unknown, code: string): boolean {
  return err instanceof ApiError && err.code === code;
}

/** Human readable message for any thrown value. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}

export function isStatus(err: unknown, status: number): boolean {
  return err instanceof ApiError && err.status === status;
}

/** The projects named by a 409 in_use (a machine that is in use cannot be archived or deleted), or null. */
export function inUseBy(err: unknown): MachineUser[] | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  const list = err.body.projects;
  if (!Array.isArray(list) && err.code !== 'in_use' && err.message !== 'in_use') return null;
  return (Array.isArray(list) ? list : []).filter((p): p is MachineUser => !!p && typeof p.id === 'string').map((p) => ({ id: p.id, name: typeof p.name === 'string' ? p.name : p.id }));
}

// ---- 401 handling -----------------------------------------------------------

type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler = () => {
  window.location.assign('/login');
};

/** App registers this to switch to the login screen without a full reload. */
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  unauthorizedHandler = fn;
}

function onAuthPage(): boolean {
  const p = window.location.pathname;
  return p === '/login' || p === '/setup';
}

// ---- fetch wrapper ----------------------------------------------------------

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

interface RequestOptions {
  /** Do not trigger the global 401 → /login redirect (caller handles it). */
  noRedirect?: boolean;
  signal?: AbortSignal;
  /** Send `body` as-is (Blob) with this Content-Type instead of JSON. */
  rawType?: string;
  /** Content-Encoding of a raw body (lib/pack.ts). */
  encoding?: string;
}

/**
 * Reads of files and git take as long as the machine takes, so they share four of the browser's
 * six connections. The other two stay free for everything else: installing an extension, saving
 * a file, a commit, the board. Without this, a hanging machine holds all of those in line.
 */
const machineReadSlots = limiter(4);

function isMachineRead(method: Method, path: string): boolean {
  if (!/^\/api\/(machines\/[^/]+\/fs|projects\/[^/]+\/(git|files|eslint))(\/|\?|$)/.test(path)) return false;
  return method === 'GET' || (method === 'POST' && /\/(stat-batch|watch|ignored|search|eslint|blame)(\?|$)/.test(path));
}

function request<T>(method: Method, path: string, body?: unknown, opts?: RequestOptions): Promise<T> {
  if (isMachineRead(method, path)) return machineReadSlots(() => send<T>(method, path, body, opts));
  return send<T>(method, path, body, opts);
}

async function send<T>(method: Method, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') headers['X-NextBoard'] = '1';
  if (body !== undefined) headers['Content-Type'] = opts.rawType ?? 'application/json';
  if (opts.encoding) headers['Content-Encoding'] = opts.encoding;

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : opts.rawType ? (body as Blob) : JSON.stringify(body),
      signal: opts.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'Cannot reach the hub (network error)');
  }

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }

  if (!res.ok) {
    const obj = data && typeof data === 'object' ? (data as { error?: unknown; code?: unknown }) : {};
    const msg = typeof obj.error === 'string' ? obj.error : res.statusText || `HTTP ${res.status}`;
    if (res.status === 401 && !opts.noRedirect && !onAuthPage()) {
      unauthorizedHandler();
    }
    throw new ApiError(res.status, msg, typeof obj.code === 'string' ? obj.code : '', obj as Record<string, unknown>);
  }
  return data as T;
}

const get = <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, undefined, opts);
const post = <T>(path: string, body?: unknown, opts?: RequestOptions) => request<T>('POST', path, body ?? {}, opts);
const put = <T>(path: string, body: unknown) => request<T>('PUT', path, body);
const patch = <T>(path: string, body: unknown) => request<T>('PATCH', path, body);
const del = <T>(path: string) => request<T>('DELETE', path);

const enc = encodeURIComponent;

/** Low-level JSON request (Phase 6 modules build their own endpoint tables on it). */
export function apiRequest<T>(method: Method, path: string, body?: unknown, opts?: RequestOptions): Promise<T> {
  return request<T>(method, path, body, opts);
}
export type { Method as HttpMethod };

type Ok = { ok: boolean };

/** POST /api/sessions/{id}/restart (full restart, §3.3). */
export interface RestartBody {
  resume?: boolean;
  mode?: Exclude<LaunchMode, 'shell'> | 'shell';
  agent?: string;
  profileId?: string;
  /** An API provider instead of a login (docs/API-agents.md §10.2). */
  providerId?: string;
  yolo?: boolean;
  /** Move the console to this conversation (mode defaults to resume). */
  agentSessionId?: string;
  /** undefined keeps what the console has, '' = the CLI's own choice. */
  model?: string;
  effort?: string;
}

// ---- endpoints ----------------------------------------------------------------

export const api = {
  // health / hub / setup
  health: () => get<Health>('/api/health', { noRedirect: true }),
  setupStatus: () => get<SetupStatus>('/api/setup/status', { noRedirect: true }),
  setup: (code: string, password: string, hubName: string) =>
    post<Ok>('/api/setup', { code, password, hubName }, { noRedirect: true }),
  hub: () => get<Hub>('/api/hub'),
  /** The instances of This PC and the ports a new one would get (admin). */
  instances: () => get<InstancesList>('/api/instances'),
  /** New instance… (admin, this instance's password again): its single-use setup link. */
  createInstance: (body: NewInstanceBody) => post<CreatedInstance>('/api/instances', body),
  /** Whether a newer release is out; the hub asks GitHub at most once an hour. */
  /** fresh: About this hub's Check for updates, GitHub asked now (not within 30 s of the last check). */
  hubUpdate: (fresh = false) => get<HubUpdate>(fresh ? '/api/hub/update?fresh=1' : '/api/hub/update'),
  /** Install it (admin): a job, then the hub restarts. */
  startHubUpdate: (version: string) => post<Job>('/api/hub/update', { version }),
  /** The update popup's answer (admin): skip that release for good, or put it off for a day. */
  remindHubUpdate: (body: { action: 'skip'; version: string } | { action: 'later' }) => post<UpdateReminder>('/api/hub/update/remind', body),

  // auth
  login: (password: string) => post<Ok>('/api/auth/login', { password }, { noRedirect: true }),
  logout: () => post<Ok>('/api/auth/logout', undefined, { noRedirect: true }),
  me: (opts?: RequestOptions) => get<Me>('/api/auth/me', opts),

  // pairing & devices
  pairStart: () => post<PairStart>('/api/pair/start'),
  devices: () => get<Device[] | null>('/api/devices').then((d) => d ?? []),
  renameDevice: (id: string, name: string) => patch<Device>(`/api/devices/${enc(id)}`, { name }),
  revokeDevice: (id: string) => del<Ok>(`/api/devices/${enc(id)}`),

  // machines
  machines: () => get<Machine[] | null>('/api/machines').then((m) => m ?? []),
  sshConfigHosts: () => get<SSHConfigHost[] | null>('/api/machines/sshconfig').then((h) => h ?? []),
  addMachine: (body: AddMachine) => post<Machine>('/api/machines', body),
  acceptHostKey: (id: string, fingerprint: string) =>
    post<Machine>(`/api/machines/${enc(id)}/hostkey`, { accept: true, fingerprint }),
  answerMachineAuth: (id: string, body: { kind: 'password' | 'passphrase'; value: string; remember: boolean }) =>
    post<Machine>(`/api/machines/${enc(id)}/credentials`, body),
  connectMachine: (id: string) => post<Machine>(`/api/machines/${enc(id)}/connect`),
  probeMachine: (id: string) => post<Machine>(`/api/machines/${enc(id)}/probe`),
  updateMachine: (id: string, body: { name?: string; persistMode?: PersistMode }) =>
    patch<Machine>(`/api/machines/${enc(id)}`, body),
  /** Archived machines only, newest first (docs/API.md Machines). */
  archivedMachines: () => get<Machine[] | null>('/api/machines?archived=1').then((m) => m ?? []),
  archiveMachine: (id: string) => post<Machine>(`/api/machines/${enc(id)}/archive`),
  restoreMachine: (id: string) => post<Machine>(`/api/machines/${enc(id)}/restore`),
  /** Without `permanent` the hub archives it. A machine in use answers 409 in_use with its projects. */
  deleteMachine: (id: string, permanent = false) => del<Ok>(`/api/machines/${enc(id)}${permanent ? '?permanent=1' : ''}`),
  bulkMachines: (ids: string[], action: BulkAction) => post<BulkResponse>('/api/machines/bulk', { ids, action }).then(bulkResults),
  machinePresets: (id: string) => get<Preset[] | null>(`/api/machines/${enc(id)}/presets`).then((p) => p ?? []),
  listDir: (id: string, path: string, signal?: AbortSignal) =>
    get<DirListing>(`/api/machines/${enc(id)}/ls?path=${enc(path)}`, { signal }),
  // names: this hub's own; legacy: no hub mark (from before hubIds), maybe another hub's live consoles
  orphans: (id: string) =>
    get<{ names: string[] | null; legacy?: string[] | null }>(`/api/machines/${enc(id)}/orphans`).then((r) => ({
      names: r.names ?? [],
      legacy: r.legacy ?? [],
    })),
  cleanup: (id: string, names: string[]) =>
    post<{ killed: string[] | null }>(`/api/machines/${enc(id)}/cleanup`, { names }).then((r) => r.killed ?? []),
  mcpServers: () => get<{ items: McpServer[] | null }>('/api/mcp/servers').then((r) => r.items ?? []),
  createMcpServer: (body: McpServer) => post<McpServer>('/api/mcp/servers', body),
  updateMcpServer: (id: string, body: McpServer) => apiRequest<McpServer>('PUT', `/api/mcp/servers/${enc(id)}`, body),
  deleteMcpServer: (id: string) => del<Ok>(`/api/mcp/servers/${enc(id)}`),
  testMcpServer: (id: string) => post<McpProbe>(`/api/mcp/servers/${enc(id)}/test`),
  connectionLog: (id: string) =>
    get<ConnLogView>(`/api/logs/connections?machineId=${enc(id)}`).then((r) => ({ ...r, items: r.items ?? [] })),

  // sessions
  presets: () => get<Preset[] | null>('/api/presets').then((p) => p ?? []),
  sessions: () => get<Session[] | null>('/api/sessions').then((s) => s ?? []),
  createSession: (body: CreateSession) => post<Session>('/api/sessions', body),
  agentModels: (agentId: string, profileId?: string) =>
    get<ModelList>(`/api/machines/local/agents/${enc(agentId)}/models${profileId ? `?profileId=${enc(profileId)}` : ''}`),
  session: (id: string) => get<Session>(`/api/sessions/${enc(id)}`),
  renameSession: (id: string, name: string) => patch<Session>(`/api/sessions/${enc(id)}`, { name }),
  deleteSession: (id: string) => del<Ok>(`/api/sessions/${enc(id)}`),
  restartSession: (id: string, body: RestartBody) => post<Session>(`/api/sessions/${enc(id)}/restart`, body),
  setHandle: (id: string, handle: string) => patch<Session>(`/api/sessions/${enc(id)}`, { handle }),
  /** Start the agent again from the shell (§3.3). */
  agentLaunch: (id: string, mode: Exclude<LaunchMode, 'shell'>, agentSessionId?: string) =>
    post<Session>(`/api/sessions/${enc(id)}/agent`, agentSessionId ? { mode, agentSessionId } : { mode }),
  reply: (id: string, signal?: AbortSignal) => get<Reply>(`/api/sessions/${enc(id)}/reply`, { signal }),
  saveReply: (id: string, dir?: string) => post<FsStat>(`/api/sessions/${enc(id)}/reply/save`, dir ? { dir } : {}),

  // agents (API-agents §1)
  agents: () => get<Agent[] | null>('/api/agents').then((a) => a ?? []),
  sandboxStatus: () => get<SandboxStatus>('/api/agents/sandbox'),
  /** With the ones turned off, flagged: useMachineAgents leaves them out of the pickers. */
  machineAgents: (id: string) => get<MachineAgents>(`/api/machines/${enc(id)}/agents?all=1`),
  createAgent: (body: AgentInput) => post<Agent>('/api/agents', body),
  updateAgent: (id: string, body: Partial<AgentInput>) => patch<Agent>(`/api/agents/${enc(id)}`, body),
  deleteAgent: (id: string) => del<Ok>(`/api/agents/${enc(id)}`),
  conversations: (machineId: string, q: { agent: string; cwd: string; profileId?: string; limit?: number }, signal?: AbortSignal) =>
    get<ConversationList>(
      `/api/machines/${enc(machineId)}/conversations?agent=${enc(q.agent)}&cwd=${enc(q.cwd)}${q.profileId ? `&profileId=${enc(q.profileId)}` : ''}&limit=${q.limit ?? 50}`,
      { signal },
    ),

  // profiles (§10)
  profiles: (machineId?: string) =>
    get<Profile[] | null>(`/api/profiles${machineId ? `?machineId=${enc(machineId)}` : ''}`).then((p) => p ?? []),
  createProfile: (body: { machineId: string; agent: string; name: string; dir?: string; create?: boolean }) =>
    post<Profile>('/api/profiles', body),
  updateProfile: (id: string, body: { name: string }) => patch<Profile>(`/api/profiles/${enc(id)}`, body),
  deleteProfile: (id: string, purge = false) => del<Ok>(`/api/profiles/${enc(id)}${purge ? '?purge=1' : ''}`),
  /** Starts the profile's CLI in a console without a tile, to sign in there (This PC only). */
  profileSignIn: (id: string) => post<Session>(`/api/profiles/${enc(id)}/signin`),

  // settings (§11) & attention (§12)
  settings: () => get<Settings>('/api/settings'),
  patchSettings: (patch: Record<string, unknown>) => request<Settings>('PATCH', '/api/settings', patch),
  attention: (since?: number, limit = 50) =>
    get<AttentionList>(`/api/attention?limit=${limit}${since !== undefined ? `&since=${since}` : ''}`),
  killSession: (id: string) => post<Session>(`/api/sessions/${enc(id)}/kill`),
  /** Sleeping agents (ROADMAP #50, API-agents §28). 409 cannot_sleep says why ("reason"). */
  sleepSession: (id: string) => post<Session>(`/api/sessions/${enc(id)}/sleep`),
  wakeSession: (id: string) => post<Session>(`/api/sessions/${enc(id)}/wake`),
  keepAwake: (id: string, keepAwake: boolean) => patch<Session>(`/api/sessions/${enc(id)}`, { keepAwake }),
  dismissDraft: (id: string) => del<Session>(`/api/sessions/${enc(id)}/draft`),
  /** Start the asleep agents of a project or one board before it is on screen (ROADMAP #50). */
  wakeProject: (id: string, workspaceId?: string) => post<{ woken: number }>(`/api/projects/${enc(id)}/wake`, workspaceId ? { workspaceId } : {}),
  sendInput: (id: string, data: string) => post<Ok>(`/api/sessions/${enc(id)}/input`, { data }),

  // projects & workspaces (API-board §1)
  projects: () => get<Project[] | null>('/api/projects').then((p) => p ?? []),
  /** With partName or parts, the project is made in several parts at once; an error about one
   *  folder carries `part`, its index (0 = the project's own folder). */
  createProject: (body: { machineId: string; path: string; name?: string; partName?: string; parts?: { name: string; machineId: string; path: string }[] }) =>
    post<Project>('/api/projects', body),
  /** Archived projects only, archived last first (API-board §1). */
  archivedProjects: () => get<Project[] | null>('/api/projects?archived=1').then((p) => p ?? []),
  updateProject: (
    id: string,
    body: { name?: string; opened?: boolean; groupId?: string; tags?: string[]; badgeId?: string; tagline?: string; sandbox?: string; partAccess?: string; cover?: { assetId: string } | null; icon?: { assetId: string } | null },
  ) => patch<Project>(`/api/projects/${enc(id)}`, body),
  archiveProject: (id: string) => post<Project>(`/api/projects/${enc(id)}/archive`),
  /** 409 not_archived when it was not. */
  restoreProject: (id: string) => post<Project>(`/api/projects/${enc(id)}/restore`),
  bulkProjects: (ids: string[], action: BulkAction) => post<BulkResponse>('/api/projects/bulk', { ids, action }).then(bulkResults),
  /** Whether "Create with AI" can make an image (Codex on This PC with image generation on). */
  artCapability: () => get<ArtCapability>('/api/art/capability'),
  /** 202 with the job; 409 while one runs for that project and kind, 412 no_image_tool. */
  createArt: (id: string, kind: ArtKind) => post<Job>(`/api/projects/${enc(id)}/art`, { kind }),
  job: (id: string) => get<Job>(`/api/jobs/${enc(id)}`),
  cancelJob: (id: string) => post<Job>(`/api/jobs/${enc(id)}/cancel`),
  createPart: (id: string, body: { name: string; machineId: string; path: string }) => post<Project>(`/api/projects/${enc(id)}/parts`, body),
  updatePart: (id: string, part: string, body: { name?: string; readOnly?: boolean; blocked?: string[]; index?: number }) => patch<Project>(`/api/projects/${enc(id)}/parts/${enc(part)}`, body),
  deletePart: (id: string, part: string) => del<Project>(`/api/projects/${enc(id)}/parts/${enc(part)}`),
  splitPart: (id: string, part: string) => post<{ project: Project; from: Project }>(`/api/projects/${enc(id)}/parts/${enc(part)}/split`, {}),
  mergePreview: (id: string, from: string) => get<MergePreview>(`/api/projects/${enc(id)}/merge?from=${enc(from)}`),
  merge: (id: string, from: string, critical: 'append' | 'drop') => post<Project>(`/api/projects/${enc(id)}/merge`, { from, critical }),
  createProjectGroup: (body: { name: string; parentId?: string }) => post<ProjectGroup>('/api/project-groups', body),
  updateProjectGroup: (id: string, body: { name?: string; parentId?: string }) => patch<ProjectGroup>(`/api/project-groups/${enc(id)}`, body),
  deleteProjectGroup: (id: string) => del<{ ok: boolean }>(`/api/project-groups/${enc(id)}`),
  createProjectBadge: (body: { name: string; color: string; icon?: string }) => post<ProjectBadge>('/api/project-badges', body),
  updateProjectBadge: (id: string, body: { name?: string; color?: string; icon?: string }) => patch<ProjectBadge>(`/api/project-badges/${enc(id)}`, body),
  /** The projects that carried it carry none. */
  deleteProjectBadge: (id: string) => del<{ ok: boolean }>(`/api/project-badges/${enc(id)}`),
  /** What is inside each group moves up a level. */
  bulkProjectGroups: (ids: string[]) => post<BulkResponse>('/api/project-groups/bulk', { ids, action: 'delete' }).then(bulkResults),
  /** Without `permanent` the hub archives it (the safe default); folders on disk are never touched. */
  deleteProject: (id: string, permanent = false) => del<Ok>(`/api/projects/${enc(id)}${permanent ? '?permanent=1' : ''}`),
  projectGit: (id: string, signal?: AbortSignal) => get<GitInfo>(`/api/projects/${enc(id)}/git`, { signal }),
  createWorkspace: (projectId: string, name?: string) =>
    post<Workspace>(`/api/projects/${enc(projectId)}/workspaces`, name ? { name } : {}),
  updateWorkspace: (id: string, body: { name?: string; index?: number }) => patch<Workspace>(`/api/workspaces/${enc(id)}`, body),
  deleteWorkspace: (id: string) => del<Ok>(`/api/workspaces/${enc(id)}`),
  closeAllPreview: (projectId: string) => get<CloseAllPreview>(`/api/projects/${enc(projectId)}/close-all`),
  closeAllBoards: (projectId: string) => post<CloseAllResult>(`/api/projects/${enc(projectId)}/close-all`, {}),

  // board v2 (API-board §2)
  board: (workspaceId: string, signal?: AbortSignal) => get<Board>(`/api/workspaces/${enc(workspaceId)}/board`, { signal }),
  postOps: (workspaceId: string, body: OpsRequest) =>
    post<{ rev: number }>(`/api/workspaces/${enc(workspaceId)}/board/ops`, body),
  snapshots: (workspaceId: string) =>
    get<SnapshotInfo[] | null>(`/api/workspaces/${enc(workspaceId)}/snapshots`).then((s) => s ?? []),
  restoreSnapshot: (workspaceId: string, snapshotId: string) =>
    post<Board>(`/api/workspaces/${enc(workspaceId)}/snapshots/${enc(snapshotId)}/restore`),
  archive: () => get<ArchiveEntry[] | null>('/api/archive').then((a) => a ?? []),
  restoreArchive: (id: string, projectId: string) => post<Workspace>(`/api/archive/${enc(id)}/restore`, { projectId }),

  // machine files (API-board §4)
  fsStat: (machineId: string, path: string, signal?: AbortSignal) =>
    get<FsStat>(`/api/machines/${enc(machineId)}/fs/stat?path=${enc(path)}`, { signal }),
  fsRead: (machineId: string, path: string, max?: number, signal?: AbortSignal) =>
    get<FsRead>(`/api/machines/${enc(machineId)}/fs/read?path=${enc(path)}${max ? `&max=${max}` : ''}`, { signal }),
  fsWrite: (machineId: string, body: FsWrite) => put<FsStat>(`/api/machines/${enc(machineId)}/fs/write`, body),
  /** A file from the computer, as raw bytes (packed by lib/pack.ts), not base64 in JSON. */
  fsUpload: (machineId: string, file: Packed, o: FsUpload) => {
    const q = new URLSearchParams({ path: o.path });
    if (o.unique) q.set('unique', '1');
    if (o.ifMtime) q.set('ifMtime', o.ifMtime);
    if (o.projectId) q.set('projectId', o.projectId);
    if (o.allowOutside) q.set('allowOutside', '1');
    for (const p of o.confirm?.paths ?? []) q.append('confirm', p);
    return request<FsStat>('PUT', `/api/machines/${enc(machineId)}/fs/write?${q}`, file.body, { rawType: 'application/octet-stream', encoding: file.encoding });
  },

  // assets (API-board §5)
  uploadAsset: (blob: Blob, type: string) => request<Asset>('POST', '/api/assets', blob, { rawType: type }),
  /** The same, packed by lib/pack.ts (gzip only where it saves something; images go as they are). */
  uploadAssetPacked: (file: Packed, type: string) => request<Asset>('POST', '/api/assets', file.body, { rawType: type, encoding: file.encoding }),
  /** A file pasted into a console lands on its machine (docs/API-studio.md §7.9). */
  attachToSession: async (id: string, file: File) => {
    const p = await packUpload(file);
    return request<FsStat>('POST', `/api/sessions/${enc(id)}/attach?name=${enc(file.name)}`, p.body, { rawType: file.type || 'application/octet-stream', encoding: p.encoding });
  },
  /** A file pasted into an order lands on This PC, where agents run. */
  attachToProject: async (id: string, file: File) => {
    const p = await packUpload(file);
    return request<FsStat>('POST', `/api/projects/${enc(id)}/attach?name=${enc(file.name)}`, p.body, { rawType: file.type || 'application/octet-stream', encoding: p.encoding });
  },
};

function bulkResults(r: BulkResponse): BulkResult[] {
  return r.results ?? [];
}

/** Where an upload goes; confirm echoes the paths of a 428 (§9.2). */
export interface FsUpload {
  path: string;
  unique?: boolean;
  ifMtime?: string;
  projectId?: string;
  allowOutside?: boolean;
  confirm?: { paths: string[] };
}

/** URL of the raw bytes of a file on a machine (images, downloads). */
export function fsRawUrl(machineId: string, path: string): string {
  return `/api/machines/${enc(machineId)}/fs/raw?path=${enc(path)}`;
}

/** URL of a file pasted into an order of this project. */
export function projectAttachUrl(projectId: string, name: string): string {
  return `/api/projects/${enc(projectId)}/attach/${enc(name)}`;
}

/** URL of an uploaded board asset. */
export function assetUrl(id: string): string {
  return `/api/assets/${enc(id)}`;
}

// ---- WebSockets -----------------------------------------------------------------

/** Close code the hub uses for revoked devices / expired tokens. */
export const WS_CLOSE_UNAUTHORIZED = 4401;

type SocketCallbacks = Omit<ReconnectingSocketOptions, 'path' | 'binaryType'>;

/**
 * When a socket is closed with 4401 (or repeatedly fails), ask /api/auth/me:
 * a 401 there triggers the normal redirect to /login.
 */
function checkAuthOnClose(code: number) {
  if (code === WS_CLOSE_UNAUTHORIZED) {
    api.me().catch(() => undefined);
  }
}

/** /api/events — JSON text frames (see HubEvent). */
export function openEventsSocket(cb: SocketCallbacks): ReconnectingSocket {
  return new ReconnectingSocket({
    path: '/api/events',
    ...cb,
    onClose: (code) => {
      checkAuthOnClose(code);
      cb.onClose?.(code);
    },
  });
}

/** /api/sessions/{id}/ws — binary output frames + JSON status frames. */
export function openSessionSocket(sessionId: string, cb: SocketCallbacks): ReconnectingSocket {
  return new ReconnectingSocket({
    path: `/api/sessions/${enc(sessionId)}/ws`,
    binaryType: 'arraybuffer',
    ...cb,
    onClose: (code) => {
      checkAuthOnClose(code);
      cb.onClose?.(code);
    },
  });
}
