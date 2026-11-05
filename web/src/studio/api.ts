// Phase 7 endpoints (docs/API-studio.md §3–§10). Errors are ApiError (status, code and
// the parsed body: 428 {"machine","paths"}, 409 {"paths"}).

import { apiRequest, type FsStat } from '../api';
import type { Job } from '../orch/types';
import type {
  DiagramListItem,
  DiagramRecord,
  DiagramStyle,
  DiagramSubject,
  DocStats,
  DocType,
  DocVersion,
  Effort,
  FindItem,
  GitBranch,
  GitCommit, GitCommitDetail, GitIndexFile, GitPullDetail, GitPullSummary, GitStash, ListeningPort, LocalVersion, ProjectTask, ReviewFile, ReviewSummary,
  GitDiff,
  GitHubStatus,
  GitRepoList,
  GitStatus,
  GitTarget,
  GitTrashItem,
  MdBacklinks,
  MdGraph,
  MdVault,
  PortProbe,
  Preview,
  PreviewLogEntry,
  Skill,
  SkillStarter,
  StudioDocument,
  TokenChart,
  TreeListing,
  UsageReport,
} from './types';

const enc = encodeURIComponent;
const get = <T>(p: string) => apiRequest<T>('GET', p);
const post = <T>(p: string, b: unknown = {}) => apiRequest<T>('POST', p, b);
const put = <T>(p: string, b: unknown) => apiRequest<T>('PUT', p, b);
const del = <T>(p: string, b?: unknown) => apiRequest<T>('DELETE', p, b);
const list = <T>(r: { items?: T[] | null } | T[] | null | undefined): T[] => (Array.isArray(r) ? r : (r?.items ?? []));

type Ok = { ok: boolean };

/** Remote-write confirmation (§9.2): echo exactly the paths the hub listed in its 428. */
export interface Confirm {
  confirm?: { paths: string[] };
}

/** Query string of a Git call: the repository, then the call's own parameters. */
const gq = (t: GitTarget, query = '') => {
  const q = [t.repo && `repo=${enc(t.repo)}`, query].filter(Boolean).join('&');
  return q ? `?${q}` : '';
};
const g = (t: GitTarget, tail: string, query = '') => `/api/projects/${enc(t.pid)}/git${tail}${gq(t, query)}`;
const paths = (ps: string[]) => ps.map((p) => `path=${enc(p)}`).join('&');

export interface GitBlameCommit {
  hash: string;
  author: string;
  email: string;
  at: string;
  summary: string;
}

export interface GitBlame {
  commits: GitBlameCommit[];
  /** Per line of the file, from the first: its index in commits. */
  lines: number[];
  me: string;
}

export const gitApi = {
  repos: (pid: string) => get<GitRepoList>(g({ pid, repo: '' }, '/repos')),
  status: (t: GitTarget) => get<GitStatus>(g(t, '/status')),
  diff: (t: GitTarget, path: string, staged = false) => get<GitDiff>(g(t, '/diff', `path=${enc(path)}&staged=${staged ? 1 : 0}`)),
  show: (t: GitTarget, path: string, ref = 'HEAD') => get<GitFileAt>(g(t, '/show', `path=${enc(path)}&ref=${enc(ref)}`)),
  log: (t: GitTarget, limit = 100) => get<{ items: GitCommit[] | null }>(g(t, '/log', `limit=${limit}`)).then(list),
  graph: (t: GitTarget, o: { all: boolean; skip: number; limit: number }) => get<{ items: GitCommit[] | null }>(g(t, '/log', `graph=1&all=${o.all ? 1 : 0}&skip=${o.skip}&limit=${o.limit}`)).then(list),
  commitDetail: (t: GitTarget, ref: string) => get<GitCommitDetail>(g(t, `/commits/${enc(ref)}`)),
  fileLog: (t: GitTarget, path: string, line?: number, limit = 100) => get<{ items: GitCommit[] | null }>(g(t, '/log', `path=${enc(path)}&limit=${limit}${line ? `&line=${line}` : ''}`)).then(list),
  /** With text: that text, an editor's unsaved state, against HEAD. */
  blame: (t: GitTarget, path: string, text?: string) => post<GitBlame>(g(t, '/blame'), text === undefined ? { path } : { path, text }),
  pulls: (t: GitTarget) => get<{ items: GitPullSummary[] | null }>(g(t, '/pulls')).then(list),
  pullRequest: (t: GitTarget, n: number) => get<GitPullDetail>(g(t, `/pulls/${n}`)),
  pullDiff: (t: GitTarget, n: number) => get<{ diff: string }>(g(t, `/pulls/${n}/diff`)),
  pullCreate: (t: GitTarget, body: { title: string; body: string; base: string; draft: boolean }) => post<{ url: string }>(g(t, '/pulls'), body),
  pullCheckout: (t: GitTarget, n: number) => post<GitStatus>(g(t, `/pulls/${n}/checkout`)),
  pullReview: (t: GitTarget, n: number, event: 'approve' | 'comment' | 'request-changes', body: string) => post<Ok>(g(t, `/pulls/${n}/review`), { event, body }),
  pullMerge: (t: GitTarget, n: number, method: 'merge' | 'squash' | 'rebase', deleteBranch: boolean) => post<GitStatus>(g(t, `/pulls/${n}/merge`), { method, deleteBranch }),
  index: (t: GitTarget, path: string) => get<GitIndexFile>(g(t, '/index', `path=${enc(path)}`)),
  stage: (t: GitTarget, paths: string[], text?: string) => post<GitStatus>(g(t, '/stage'), text === undefined ? { paths } : { paths, text }),
  unstage: (t: GitTarget, paths: string[]) => post<GitStatus>(g(t, '/unstage'), { paths }),
  stashes: (t: GitTarget) => get<{ items: GitStash[] | null }>(g(t, '/stash')).then(list),
  stashSave: (t: GitTarget, message: string, untracked: boolean) => post<GitStatus>(g(t, '/stash'), { message, untracked }),
  stashAct: (t: GitTarget, action: 'apply' | 'pop' | 'drop', ref: string) => post<GitStatus>(g(t, `/stash/${action}`), { ref }),
  commitOp: (t: GitTarget, body: { op: 'checkout' | 'cherry-pick' | 'revert' | 'merge' | 'reset'; ref: string; mode?: 'soft' | 'mixed' | 'hard' }) => post<GitStatus>(g(t, '/commit-op'), body),
  tag: (t: GitTarget, body: { name: string; ref: string; message?: string }) => post<GitStatus>(g(t, '/tags'), body),
  operation: (t: GitTarget, action: 'abort' | 'continue') => post<GitStatus>(g(t, `/operation/${action}`)),
  commit: (t: GitTarget, body: { message: string; paths?: string[]; all?: boolean; staged?: boolean; allowLarge?: boolean }) =>
    post<{ commit: GitCommit; status: GitStatus }>(g(t, '/commit'), body),
  push: (t: GitTarget, setUpstream?: boolean) => post<Job>(g(t, '/push'), setUpstream ? { setUpstream } : {}),
  pull: (t: GitTarget) => post<Job>(g(t, '/pull')),
  fetch: (t: GitTarget) => post<Job>(g(t, '/fetch')),
  discard: (t: GitTarget, paths: string[]) => post<{ trashedTo: string; trashed?: string[]; status: GitStatus }>(g(t, '/discard'), { paths }),
  trash: (t: GitTarget) => get<{ items: GitTrashItem[] | null }>(g(t, '/trash')).then(list),
  restore: (t: GitTarget, id: string, paths?: string[]) => post<{ restored: string[] | null }>(g(t, '/trash/restore'), paths ? { id, paths } : { id }),
  init: (t: GitTarget) => post<GitStatus>(g(t, '/init'), { branch: 'main' }),
  untrackLarge: (t: GitTarget, paths: string[]) => post<GitStatus>(g(t, '/untrack-large'), { paths }),
  branches: (t: GitTarget) => get<{ current: string; items: GitBranch[] | null }>(g(t, '/branches')),
  createBranch: (t: GitTarget, name: string, sw = true, from?: string) => post<GitStatus>(g(t, '/branches'), from ? { name, switch: sw, from } : { name, switch: sw }),
  switchBranch: (t: GitTarget, name: string) => post<GitStatus>(g(t, '/switch'), { name }),
  deleteBranch: (t: GitTarget, name: string) => del<Ok>(g(t, `/branches/${name.split('/').map(enc).join('/')}`)),
  suggest: (t: GitTarget, ps: string[] = []) => get<{ subject: string; source: string }>(g(t, '/suggest', ['ai=0', paths(ps)].filter(Boolean).join('&'))),
  suggestAI: (t: GitTarget, ps: string[] = [], staged = false) => get<Job>(g(t, '/suggest', ['ai=1', staged ? 'staged=1' : '', paths(ps)].filter(Boolean).join('&'))),
  selection: (t: GitTarget, paths: string[]) => put<{ paths: string[] }>(g(t, '/selection'), { paths }),
  github: (t: GitTarget) => get<GitHubStatus>(`/api/projects/${enc(t.pid)}/github${gq(t)}`),
  githubLogin: (pid: string, workspaceId?: string) => post<{ id: string }>(`/api/projects/${enc(pid)}/github/login`, workspaceId ? { workspaceId } : {}),
  publish: (t: GitTarget, body: { name: string; visibility: 'private' | 'public'; commitAll: boolean; message?: string }) =>
    post<Job>(`/api/projects/${enc(t.pid)}/github/publish${gq(t)}`, body),
};

export const portsApi = {
  listening: (machineId: string) => get<{ items: ListeningPort[] | null }>(`/api/machines/${enc(machineId)}/listening`).then(list),
};

export const taskApi = {
  list: (t: GitTarget) => get<{ items: ProjectTask[] | null }>(`/api/projects/${enc(t.pid)}/tasks${gq(t)}`).then(list),
  run: (t: GitTarget, id: string) => post<Job>(`/api/projects/${enc(t.pid)}/tasks/run${gq(t)}`, { id }),
};

const rv = (pid: string, tail: string) => `/api/projects/${enc(pid)}${tail}`;

export const reviewApi = {
  list: (pid: string) => get<ReviewSummary>(rv(pid, '/review')),
  file: (pid: string, session: string, path: string) => get<ReviewFile>(rv(pid, `/review/file?session=${enc(session)}&path=${enc(path)}`)),
  accept: (pid: string, body: { session: string; path?: string; base?: string }) => post<Ok>(rv(pid, '/review/accept'), body),
  reject: (pid: string, body: { session: string; path?: string; text?: string }) => post<Ok>(rv(pid, '/review/reject'), body),
  history: (pid: string, path: string) => get<{ items: LocalVersion[] | null }>(rv(pid, `/history?path=${enc(path)}`)).then(list),
  version: (pid: string, path: string, commit: string) => get<{ content: string; binary: boolean }>(rv(pid, `/history/version?path=${enc(path)}&commit=${enc(commit)}`)),
};

export const previewApi = {
  probe: (machineId: string, ports: number[]) =>
    get<PortProbe[] | null>(`/api/machines/${enc(machineId)}/ports?probe=${ports.join(',')}`).then((r) => r ?? []),
  list: () => get<Preview[] | null>('/api/previews').then((r) => r ?? []),
  create: (body: { machineId: string; port: number; scheme?: string; path?: string; sessionId?: string } | { url: string }) =>
    post<{ preview: Preview; launchPath: string }>('/api/previews', body),
  launch: (id: string, path?: string) => post<{ launchPath: string; expiresAt: string }>(`/api/previews/${enc(id)}/launch`, path ? { path } : {}),
  remove: (id: string) => del<Ok>(`/api/previews/${enc(id)}`),
  logs: (id: string, since = 0) => get<{ items: PreviewLogEntry[] | null; seq: number }>(`/api/previews/${enc(id)}/logs?since=${since}`),
};

export const docApi = {
  list: (pid: string) => get<StudioDocument[] | null>(`/api/projects/${enc(pid)}/documents`).then((r) => r ?? []),
  generate: (pid: string, body: { type: DocType | string; ask?: string; effort: Effort; agent?: string; profileId?: string; documentId?: string }) =>
    post<{ document: StudioDocument; versionId: string; job: Job }>(`/api/projects/${enc(pid)}/documents`, body),
  get: (id: string) => get<StudioDocument>(`/api/documents/${enc(id)}`),
  version: (id: string, vid: string) => get<{ version: DocVersion; markdown: string }>(`/api/documents/${enc(id)}/versions/${enc(vid)}`),
  remove: (id: string) => del<Ok>(`/api/documents/${enc(id)}`),
  removeVersion: (id: string, vid: string) => del<Ok>(`/api/documents/${enc(id)}/versions/${enc(vid)}`),
  stats: (machineId: string, agent: string) => get<DocStats>(`/api/documents/stats?machineId=${enc(machineId)}&agent=${enc(agent)}`),
};

export const diagramApi = {
  list: (pid: string) => get<DiagramListItem[] | null>(`/api/projects/${enc(pid)}/diagrams`).then((r) => r ?? []),
  generate: (pid: string, body: { subject: DiagramSubject; ask?: string; effort: Effort; style: DiagramStyle; agent?: string }) =>
    post<{ diagramId: string; job: Job }>(`/api/projects/${enc(pid)}/diagrams`, body),
  get: (id: string) => get<DiagramRecord>(`/api/diagrams/${enc(id)}`),
  land: (id: string, body: { workspaceId: string; x?: number; y?: number; style?: DiagramStyle }) =>
    post<{ rev: number; nodeIds: string[] }>(`/api/diagrams/${enc(id)}/land`, body),
};

const m = (id: string) => `/api/machines/${enc(id)}`;

export const fsApi = {
  tree: (mid: string, path: string, hidden = false) => get<TreeListing>(`${m(mid)}/fs/tree?path=${enc(path)}&hidden=${hidden ? 1 : 0}`),
  find: (mid: string, root: string, q: string, limit = 50) =>
    get<{ items: FindItem[] | null; truncated: boolean }>(`${m(mid)}/fs/find?root=${enc(root)}&q=${enc(q)}&limit=${limit}`),
  mkdir: (mid: string, path: string, c: Confirm = {}) => post<FsStat>(`${m(mid)}/fs/mkdir`, { path, ...c }),
  rename: (mid: string, from: string, to: string, c: Confirm = {}) => post<FsStat>(`${m(mid)}/fs/rename`, { from, to, ...c }),
  copy: (mid: string, from: string, to: string, unique: boolean, c: Confirm = {}) => post<FsStat>(`${m(mid)}/fs/copy`, { from, to, unique, ...c }),
  trash: (mid: string, path: string, projectId: string, c: Confirm = {}) => post<{ trashedTo: string }>(`${m(mid)}/fs/trash`, { path, projectId, ...c }),
  statBatch: (mid: string, paths: string[]) => post<FsStat[] | null>(`${m(mid)}/fs/stat-batch`, { paths }).then((r) => r ?? []),
  write: (
    mid: string,
    body: { path: string; content: string; encoding?: 'utf8' | 'base64'; ifMtime?: string; unique?: boolean; projectId?: string; allowOutside?: boolean } & Confirm,
  ) => put<FsStat>(`${m(mid)}/fs/write`, { encoding: 'utf8', ...body }),
  watch: (mid: string, paths: string[], ttlSec = 60) =>
    post<{ watchId: string; stats: FsStat[] | null; intervalMs: number }>(`${m(mid)}/fs/watch`, { paths, ttlSec }),
  renewWatch: (wid: string) => post<Ok>(`/api/fs/watch/${enc(wid)}/renew`),
  unwatch: (wid: string) => del<Ok>(`/api/fs/watch/${enc(wid)}`),
};

export interface GitFileAt {
  path: string;
  ref: string;
  exists: boolean;
  binary: boolean;
  truncated: boolean;
  content: string;
}

export interface FileListing {
  items: string[] | null;
  truncated: boolean;
  source: 'git' | 'walk' | string;
}

export interface SearchQuery {
  query: string;
  matchCase?: boolean;
  wholeWord?: boolean;
  regex?: boolean;
  include?: string;
  exclude?: string;
  limit?: number;
}

export interface SearchMatch {
  line: number;
  col: number;
  len: number;
  text: string;
  textCol: number;
}

export interface SearchFile {
  rel: string;
  matches: SearchMatch[];
}

export interface SearchResponse {
  files: SearchFile[] | null;
  count: number;
  truncated: boolean;
  engine: string;
}

export interface LspServer {
  id: string;
  name: string;
  languages: string[];
  available: boolean;
  command: string;
  install: string;
  /** Works next to its languages' main server, while its extension is installed. */
  extra?: boolean;
  /** Vue only: where TypeScript's server finds the plugin that makes it read Vue files. */
  plugin?: string;
}

export interface EslintMessage {
  ruleId: string | null;
  severity: number;
  message: string;
  line?: number;
  column?: number;
  endLine?: number;
  endColumn?: number;
  fix?: { range: [number, number]; text: string };
}

export interface EslintRun {
  available: boolean;
  error?: string;
  messages?: EslintMessage[] | null;
  output?: string;
}

export interface OnSaveRun {
  pattern: string;
  command: string;
  exitCode: number;
  timedOut: boolean;
  output: string;
}

const pr = (pid: string) => `/api/projects/${enc(pid)}`;

/** Code workbench (docs/API-studio.md §7.2–§7.4). */
export const filesApi = {
  eslint: (pid: string, path: string, text: string, fix = false) => post<EslintRun>(`/api/projects/${enc(pid)}/eslint`, { path, text, fix }),
  list: (pid: string) => get<FileListing>(`${pr(pid)}/files`),
  search: (pid: string, body: SearchQuery, signal?: AbortSignal) => apiRequest<SearchResponse>('POST', `${pr(pid)}/files/search`, body, { signal }),
  replace: (pid: string, body: SearchQuery & { replacement: string; paths: string[] } & Confirm) =>
    post<{ files: { rel: string; count: number; error?: string }[] | null; count: number }>(`${pr(pid)}/files/replace`, body),
  ignored: (pid: string, paths: string[]) => post<{ items: string[] | null }>(`${pr(pid)}/files/ignored`, { paths }),
  onSave: (pid: string, path: string) => post<{ runs: OnSaveRun[] | null }>(`${pr(pid)}/files/on-save`, { path }),
  aiEdit: (pid: string, body: { path: string; language: string; instruction: string; before: string; selection: string; after: string }) => post<Job>(`${pr(pid)}/code/edit`, body),
  lspStatus: (pid: string) => get<{ items: LspServer[] | null }>(`${pr(pid)}/lsp`),
  aiAsk: (pid: string, body: { question: string; path: string; language: string; selection: string; startLine: number; endLine: number }) => post<Job>(`${pr(pid)}/code/ask`, body),
  aiComplete: (pid: string, body: { path: string; language: string; before: string; after: string }) => post<Job>(`${pr(pid)}/code/complete`, body),
  dapStatus: (pid: string) => get<{ items: DebugAdapterInfo[] | null }>(`${pr(pid)}/dap`),
};

export interface DebugAdapterInfo {
  id: string;
  name: string;
  types: string[];
  languages: string[];
  available: boolean;
  command: string;
  install: string;
}

export interface ExtensionHit {
  namespace: string;
  name: string;
  version: string;
  displayName: string;
  description: string;
  downloadCount: number;
  /** What it would do here: its code never runs, see InstalledExtension. */
  hasCode?: boolean;
  native?: string;
  gives?: string[] | null;
}

export interface ExtensionSearch {
  items: ExtensionHit[] | null;
  /** Hits left out because they cannot work here. */
  hidden: number;
  /** Hits left out because their manifest did not arrive. */
  unchecked: number;
}

export interface InstalledExtension {
  id: string;
  namespace: string;
  name: string;
  version: string;
  displayName: string;
  description: string;
  hasCode: boolean;
  /** The work the editor does for it: a name, or "lsp:" and a language server. */
  native?: string;
  contributes: {
    themes: { label: string; uiTheme: string; path: string }[] | null;
    iconThemes?: { label: string; path: string }[] | null;
    snippets: { language: string; path: string }[] | null;
    grammars: { language: string; scopeName: string; path: string }[] | null;
    languages: { id: string; aliases: string[] | null; extensions: string[] | null; filenames: string[] | null; configuration: string }[] | null;
  };
}

/** Extensions from Open VSX: data only (docs/API-studio.md §7.6). */
export const extApi = {
  list: () => get<{ items: InstalledExtension[] | null }>('/api/extensions'),
  search: (q: string, signal?: AbortSignal) => apiRequest<ExtensionSearch>('GET', `/api/extensions/search?q=${enc(q)}`, undefined, { signal }),
  install: (namespace: string, name: string, signal?: AbortSignal) => apiRequest<InstalledExtension>('POST', '/api/extensions', { namespace, name }, { signal }),
  remove: (id: string) => del<Ok>(`/api/extensions/${enc(id)}`),
  file: async (id: string, path: string): Promise<string> => {
    const r = await fetch(`/api/extensions/${enc(id)}/file?path=${enc(path)}`);
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.text();
  },
};

export const mdApi = {
  vault: (pid: string, refresh = false) => get<MdVault>(`/api/projects/${enc(pid)}/markdown?refresh=${refresh ? 1 : 0}`),
  graph: (pid: string) => get<MdGraph>(`/api/projects/${enc(pid)}/markdown/graph`),
  backlinks: (pid: string, rel: string) => get<MdBacklinks>(`/api/projects/${enc(pid)}/markdown/backlinks?path=${enc(rel)}`),
};

export interface SkillScopeQuery {
  projectId?: string;
  profileId?: string;
}
const sq = (q: SkillScopeQuery) => `${q.projectId ? `&projectId=${enc(q.projectId)}` : ''}${q.profileId ? `&profileId=${enc(q.profileId)}` : ''}`;

export const skillApi = {
  list: (mid: string, q: SkillScopeQuery) => get<{ items: Skill[] | null; truncated: boolean; cfg?: string }>(`${m(mid)}/skills?x=1${sq(q)}`),
  item: (mid: string, path: string, q: SkillScopeQuery) => get<{ skill: Skill; content: string }>(`${m(mid)}/skills/item?path=${enc(path)}${sq(q)}`),
  lint: (mid: string, content: string, path?: string) => post<{ lint: Skill['lint'] }>(`${m(mid)}/skills/lint`, path ? { content, path } : { content }),
  write: (mid: string, body: { path: string; content: string; ifMtime?: string } & SkillScopeQuery & Confirm) => put<Skill>(`${m(mid)}/skills/item`, body),
  create: (
    mid: string,
    body: { scope: string; kind: 'skill' | 'command'; name: string; description: string; argumentHint?: string; body: string } & SkillScopeQuery & Confirm,
  ) => post<Skill>(`${m(mid)}/skills`, body),
  duplicate: (mid: string, body: { path: string; toScope: string } & SkillScopeQuery & Confirm) => post<Skill>(`${m(mid)}/skills/duplicate`, body),
  remove: (mid: string, body: { path: string } & SkillScopeQuery & Confirm) => del<{ ok: boolean; backup: string }>(`${m(mid)}/skills/item`, body),
  starters: () => get<SkillStarter[] | null>('/api/skills/starters').then((r) => r ?? []),
  run: (sessionId: string, name: string, args?: string) => post<{ id: string }>(`/api/sessions/${enc(sessionId)}/skill`, args ? { name, args } : { name }),
};

export const usageApi = {
  reports: () => get<{ items: UsageReport[] | null }>('/api/usage').then(list),
  /** 200 = cached report, 202 = a job (watch it; its result is the report). */
  check: (body: { machineId: string; agent: string; profileId?: string; maxAgeSec?: number; trigger: 'user' | 'background' }) =>
    post<UsageReport | Job>('/api/usage/check', body),
  tokens: (days = 30, machineId = '', agent = '') =>
    get<TokenChart>(`/api/usage/tokens?days=${days}${machineId ? `&machineId=${enc(machineId)}` : ''}${agent ? `&agent=${enc(agent)}` : ''}`),
};

export const jobApi = {
  get: (id: string) => get<Job>(`/api/jobs/${enc(id)}`),
  cancel: (id: string) => post<Job>(`/api/jobs/${enc(id)}/cancel`),
};

export const isJob = (v: unknown): v is Job => !!v && typeof v === 'object' && typeof (v as Job).id === 'string' && (v as Job).id.startsWith('jb_');
