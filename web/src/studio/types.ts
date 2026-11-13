// Phase 7 models (docs/API-studio.md): Git, GitHub, previews, documents, diagrams, code
// and markdown studios, skills, usage. Field names are the exact JSON names; optional
// fields are the hub's additions (§3.5, §4.8, §5.1, §6.1, §7.1, §8.1, §9.3, §10.4).

// ---- §3 Git ----------------------------------------------------------------------------

export type GitFileStatus = 'modified' | 'added' | 'deleted' | 'renamed' | 'untracked' | 'conflict' | 'typechange';

export interface GitFile {
  path: string;
  oldPath?: string;
  index: string;
  worktree: string;
  status: GitFileStatus | string;
  added: number;
  deleted: number;
  binary: boolean;
}

export interface GitLargeFile {
  path: string;
  sizeBytes: number;
  tracked?: boolean;
}

export interface GitStatus {
  isRepo: boolean;
  root?: string;
  branch: string;
  detached: boolean;
  hasCommits?: boolean;
  upstream: string;
  hasRemote: boolean;
  remoteName?: string;
  remoteUrl: string;
  webUrl: string;
  ahead: number;
  behind: number;
  fetchedAt: string | null;
  merging: boolean;
  /** What is under way and can be aborted. */
  operation?: 'merge' | 'cherry-pick' | 'revert' | string;
  conflicts: number;
  files: GitFile[] | null;
  changed?: number;
  truncated: boolean;
  largeFiles: GitLargeFile[] | null;
}

/** A path over 100 MB in commits no remote has yet. */
export interface GitLargeCommitted {
  path: string;
  sizeBytes: number;
  commits: number;
  /** A pushed commit holds this path too: removing it deletes it on the remote at the next push. */
  inPushed: boolean;
}

export interface GitLargeHistory {
  head: string;
  detached: boolean;
  unpushed: number;
  tooMany?: boolean;
  files: GitLargeCommitted[];
}

export interface GitLargeRemoved {
  backup: string;
  head: string;
  rewritten: number;
  dropped: number;
  status: GitStatus;
}

/** Compact status of the {"type":"git"} and {"type":"gitRepo"} events. */
export interface GitSummary {
  branch: string;
  ahead: number;
  behind: number;
  changed: number;
}

/** Which repository a Git call works on: `repo` is relative to the project folder, "" = the project's own. */
export interface GitTarget {
  pid: string;
  repo: string;
}

/** One repository found in a project's folder (GET …/git/repos). */
export interface GitRepoInfo extends GitSummary {
  path: string;
  name: string;
  root?: string;
  error?: string;
}

export interface GitRepoList {
  items: GitRepoInfo[];
  truncated: boolean;
}

export interface GitCommit {
  hash: string;
  short: string;
  subject: string;
  author: string;
  at: string;
  pushed: boolean;
  /** Only in the history graph. */
  parents?: string[];
  refs?: GitRef[];
}

export interface GitRef {
  name: string;
  kind: 'head' | 'branch' | 'remote' | 'tag';
}

export interface GitCommitFile {
  path: string;
  oldPath?: string;
  status: string;
  add: number;
  del: number;
  binary?: boolean;
}

export interface GitCommitDetail {
  hash: string;
  short: string;
  parents: string[];
  refs: GitRef[];
  subject: string;
  body: string;
  author: string;
  email: string;
  at: string;
  committer: string;
  committerEmail: string;
  committedAt: string;
  files: GitCommitFile[];
  truncated: boolean;
}

export interface GitPullSummary {
  number: number;
  title: string;
  url: string;
  author: { login: string };
  headRefName: string;
  baseRefName: string;
  isDraft: boolean;
  updatedAt: string;
  reviewDecision: string;
}

export interface GitPullComment {
  author: { login: string };
  body: string;
  state?: string;
  createdAt?: string;
  submittedAt?: string;
}

export interface GitPullCheck {
  name?: string;
  context?: string;
  status?: string;
  state?: string;
  conclusion?: string;
}

export interface GitPullDetail extends GitPullSummary {
  body: string;
  state: string;
  additions: number;
  deletions: number;
  changedFiles: number;
  mergeable: string;
  createdAt: string;
  statusCheckRollup: GitPullCheck[] | null;
  files: { path: string; additions: number; deletions: number }[] | null;
  comments: GitPullComment[] | null;
  reviews: GitPullComment[] | null;
}

export interface ListeningPort {
  port: number;
  address: string;
  /** Empty when the system does not say, as for another user's programs. */
  process: string;
  pid?: number;
}

export interface ProjectTask {
  id: string;
  group: string;
  name: string;
  kind: 'test' | 'build' | 'run' | string;
  command: string;
  cwd: string;
}

export interface GitStash {
  ref: string;
  hash: string;
  message: string;
  at: string;
}

export interface GitIndexFile {
  path: string;
  exists: boolean;
  binary: boolean;
  text: string;
}

export interface ReviewChange {
  path: string;
  add: number;
  del: number;
  status: string;
  binary?: boolean;
  /** Several agents were at work and no hook named this file. */
  unsure?: boolean;
  also?: string[];
  since: string;
}

export interface ReviewAgent {
  sessionId: string;
  name: string;
  kind: string;
  working: boolean;
  files: ReviewChange[];
}

export interface ReviewSummary {
  supported: boolean;
  /** Why the last look at the server failed: what is listed may be behind. */
  problem?: string;
  agents: ReviewAgent[];
}

export interface ReviewFile {
  path: string;
  base: string;
  baseExists: boolean;
  current: string;
  currentExists: boolean;
  binary: boolean;
  tooBig: boolean;
}

export interface LocalVersion {
  commit: string;
  at: string;
  who: string;
  exists: boolean;
}

export interface GitBranch {
  name: string;
  current: boolean;
  upstream: string;
  ahead: number;
  behind: number;
  gone?: boolean;
  lastCommitAt: string | null;
}

export interface GitDiff {
  path: string;
  diff: string;
  truncated: boolean;
  binary?: boolean;
}

export interface GitTrashItem {
  id: string;
  at: string;
  paths: string[] | null;
}

export interface GitHubStatus {
  ghInstalled: boolean;
  authenticated: boolean;
  user: string;
  remoteUrl: string;
  repoUrl: string;
  installHint: string;
}

// ---- §4 previews -----------------------------------------------------------------------

export interface PortProbe {
  port: number;
  open: boolean;
  http: boolean;
}

export type PreviewState = 'waiting' | 'live' | 'error';

export interface Preview {
  id: string;
  machineId: string;
  /** A site on the tailnet (docs/API-studio.md §4.9); empty for a port of the machine. */
  targetHost?: string;
  targetPort: number;
  targetScheme: string;
  path: string;
  listenPort: number;
  sessionId?: string;
  auto: boolean;
  state: PreviewState | string;
  error: string;
  createdAt: string;
  lastUsedAt: string;
}

export interface PreviewLogEntry {
  seq: number;
  at: string;
  type: 'console' | 'error' | 'net' | string;
  level?: string;
  args?: string[];
  message?: string;
  source?: string;
  line?: number;
  col?: number;
  stack?: string;
  method?: string;
  url?: string;
  status?: number;
  ms?: number;
  page?: string;
}

// ---- §5 documents ----------------------------------------------------------------------

export const DOC_TYPES = [
  'overview',
  'onboarding',
  'architecture',
  'readme',
  'api',
  'dataModel',
  'security',
  'testing',
  'releaseNotes',
  'custom',
] as const;
export type DocType = (typeof DOC_TYPES)[number];
export type Effort = 'quick' | 'balanced' | 'thorough';

export interface DocVersion {
  id: string;
  at: string;
  agent: string;
  machineId: string;
  effort: Effort | string;
  state: 'running' | 'complete' | 'incomplete' | string;
  words: number;
  durationMs: number;
  jobId: string;
}

export interface StudioDocument {
  id: string;
  projectId: string;
  type: DocType | string;
  title: string;
  ask?: string;
  versions: DocVersion[] | null;
}

export type DocStats = Record<Effort, { medianMs: number | null; runs: number }>;

// ---- §6 diagrams -----------------------------------------------------------------------

export type DiagramShape = 'box' | 'round' | 'ellipse' | 'diamond';
export interface DiagramNode {
  id: string;
  label: string;
  note?: string;
  shape: DiagramShape | string;
  group?: string;
}
export interface DiagramEdge {
  from: string;
  to: string;
  label?: string;
  style?: 'solid' | 'dashed' | string;
}
export interface DiagramGroup {
  id: string;
  label: string;
}
export interface Diagram {
  schema: 'nb.diagram/v1' | string;
  title: string;
  direction: 'TB' | 'LR' | string;
  nodes: DiagramNode[];
  edges: DiagramEdge[];
  groups: DiagramGroup[];
}

export const DIAGRAM_SUBJECTS = ['architecture', 'userFlow', 'dataFlow', 'components', 'requestLifecycle', 'stateFlow', 'custom'] as const;
export type DiagramSubject = (typeof DIAGRAM_SUBJECTS)[number];
export type DiagramStyle = 'ink' | 'color';
export type ArrowStyle = 'curved' | 'wiring';
export type ArrowColor = 'mono' | 'color';

export interface DiagramListItem {
  id: string;
  title: string;
  subject?: string;
  createdAt: string;
  agent: string;
  state?: 'running' | 'done' | 'failed' | 'cancelled' | string;
}

export interface DiagramRecord {
  id: string;
  projectId: string;
  subject: string;
  ask?: string;
  effort: string;
  style: DiagramStyle | string;
  agent: string;
  title: string;
  createdAt: string;
  state: 'running' | 'done' | 'failed' | 'cancelled' | string;
  jobId: string;
  error?: string;
  warnings?: string[] | null;
  diagram: Diagram | null;
}

// ---- §7 code studio --------------------------------------------------------------------

export interface TreeEntry {
  name: string;
  dir: boolean;
  size: number;
  mtime?: string;
  hidden: boolean;
  binary: boolean;
}
export interface TreeListing {
  path: string;
  entries: TreeEntry[] | null;
  pruned: string[] | null;
  truncated: boolean;
}
export interface FindItem {
  path: string;
  rel: string;
  dir: boolean;
  size: number;
}

// ---- §8 markdown -----------------------------------------------------------------------

export interface MdFile {
  path: string;
  rel: string;
  size: number;
  mtime: string;
  title: string;
}
export interface MdLink {
  from: string;
  to: string;
  kind: 'wiki' | 'md' | string;
  resolved: boolean;
  heading?: string;
}
export interface MdVault {
  files: MdFile[] | null;
  links: MdLink[] | null;
  agentMemory: { dir: string; files: MdFile[] | null } | null;
  truncated: boolean;
  scannedAt?: string;
}
export interface MdGraph {
  nodes: { id: string; title: string; in: number; out: number; orphan: boolean }[] | null;
  edges: { from: string; to: string; kind: string; count: number }[] | null;
  unresolved: number;
  truncated: boolean;
}
export interface MdBacklinks {
  path: string;
  backlinks: { from: string; title: string; kind: string; heading?: string }[] | null;
  outgoing: MdLink[] | null;
}

// ---- §9 skills -------------------------------------------------------------------------

export type SkillScope = 'personal' | 'project' | 'personalCommand' | 'projectCommand' | 'plugin';

export interface LintFinding {
  rule: string;
  severity: 'warn' | 'error' | 'info' | string;
  message: string;
}

export interface Skill {
  id: string;
  scope: SkillScope | string;
  kind: 'skill' | 'command' | string;
  name: string;
  plugin?: string;
  path: string;
  description: string;
  invoke: string;
  argumentHint?: string;
  modelInvocable: boolean;
  subagent: boolean;
  model?: string;
  allowedTools?: string[] | null;
  tokensEstimate: number;
  siblings: string[] | null;
  shadowedBy?: string;
  mtime: string;
  lint: LintFinding[] | null;
}

export interface SkillStarter {
  name: string;
  title: string;
  description: string;
  argumentHint?: string;
  body: string;
  content: string;
}

// ---- §10 usage -------------------------------------------------------------------------

export interface UsageWindow {
  id: string;
  label: string;
  usedPct: number;
  resetsAt: string | null;
  resetsText: string;
}

export type UsageState = 'ok' | 'needsInteractive' | 'notLoggedIn' | 'unsupported' | 'error';

export interface UsageReport {
  machineId: string;
  agent: string;
  profileId: string;
  checkedAt: string;
  trigger: 'background' | 'user' | string;
  state: UsageState | string;
  windows: UsageWindow[] | null;
  facts: { label: string; value: string }[] | null;
  raw: string[] | null;
  parsed: boolean;
  error: string;
}

export interface TokenPoint {
  date: string;
  agent: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface TokenChart {
  from: string;
  to: string;
  series: TokenPoint[] | null;
  byAgent: { agent: string; days30: { total: number; output: number }; allTime: { total: number }; sessions: number; favoriteModel: string; longestSessionSec: number }[] | null;
  busiestDay: { date: string; total: number } | null;
  top: string;
  scannedAt: string;
  partial: boolean;
}

// ---- events ----------------------------------------------------------------------------

export type StudioEvent =
  | { type: 'git'; projectId: string; status: GitSummary }
  | { type: 'gitRepo'; projectId: string; repo: string; status: GitSummary }
  /** A part of the project other than the first (ROADMAP #49). */
  | { type: 'gitPart'; projectId: string; part: string; repo: string; status: GitSummary }
  | { type: 'github'; projectId: string; github: GitHubStatus }
  | { type: 'preview'; preview: Preview }
  | { type: 'previewRemoved'; id: string }
  | { type: 'previewSuggested'; sessionId: string; previewId: string; url: string; workspaceId?: string; nodeId?: string; machineId?: string; port?: number }
  | { type: 'previewLog'; previewId: string; entries: PreviewLogEntry[] | null }
  | { type: 'review'; projectId: string }
  | { type: 'reviewOverlap'; projectId: string; path: string; agents: string[] }
  | { type: 'document'; projectId: string; document: StudioDocument }
  | { type: 'documentRemoved'; projectId: string; documentId: string }
  | { type: 'diagram'; projectId: string; diagramId: string; state: string }
  | { type: 'usage'; report: UsageReport }
  | { type: 'usageReset'; machineId: string; agent: string; profileId: string; window: string; at: string; sound?: string }
  | { type: 'skills'; machineId: string }
  | { type: 'fsChanged'; watchId: string; machineId: string; changes: { path: string; size: number; mtime: string; exists: boolean; error?: string }[] | null };
