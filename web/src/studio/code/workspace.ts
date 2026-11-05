// State of one project's Code workbench: file tree, open documents (Monaco models), editor
// groups and tabs, git decorations, the file list of "Go to file". It outlives the
// workbench's React tree so closing and reopening keeps tabs, undo and unsaved edits; the
// layout and the tab list also survive a reload (localStorage, drafts in IndexedDB).

import { api, ApiError, errorMessage, fsRawUrl, isCode } from '../../api';
import { fileToBase64, sendPasted } from '../../board/files';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { limiter } from '../../lib/limiter';
import type { Project } from '../../lib/types';
import { store } from '../../store';
import { filesApi, fsApi, gitApi, reviewApi, type Confirm, type FileListing } from '../api';
import { drafts, type Draft } from '../drafts';
import { studioStore, waitJob } from '../store';
import type { GitStatus, GitTarget, ProjectTask, ReviewSummary, TreeEntry } from '../types';
import { fileClip } from './fileClip';
import { IdeLink } from './ideLink';
import { Debugger } from './debug';
import { LspHub } from './lsp';
import { languageFor, monaco } from './monaco';

/** Files up to here are editable; larger ones are read-only up to MAX_VIEW. */
export const MAX_EDIT = 1 << 20;
export const MAX_VIEW = 10 << 20;
const MAX_GROUPS = 3;
const MAX_RECENT = 50;
const LSP_IDLE_MS = 5 * 60_000;

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'avif', 'bmp', 'svg', 'ico']);
const MEDIA_EXT = new Set(['mp4', 'webm', 'mov', 'mp3', 'wav', 'ogg', 'flac']);
export const MARKDOWN_EXT = new Set(['md', 'markdown', 'mdx']);

export type SideView = 'files' | 'search' | 'scm' | 'review' | 'debug' | 'extensions' | 'agents' | 'settings';
export type PanelView = 'terminal' | 'problems' | 'output' | 'debug' | 'tasks' | 'ports';
export type MdView = 'reader' | 'split' | 'source';
export type DiffSource = 'head' | 'index' | 'staged' | 'disk' | `commit:${string}` | `ide:${string}`;

export const extOf = (p: string) => {
  const base = p.slice(p.lastIndexOf('/') + 1);
  const i = base.lastIndexOf('.');
  return i > 0 ? base.slice(i + 1).toLowerCase() : '';
};
export const nameOf = (p: string) => p.slice(p.lastIndexOf('/') + 1);
export const dirOf = (p: string) => p.slice(0, p.lastIndexOf('/')) || '/';

export interface Doc {
  path: string;
  kind: 'text' | 'image' | 'media' | 'binary' | 'toolarge';
  status: 'loading' | 'ready' | 'error';
  error: string;
  model: monaco.editor.ITextModel | null;
  savedAlt: number;
  dirty: boolean;
  mtime: string;
  size: number;
  bom: boolean;
  /** Not UTF-8: decoded with this, read-only so that the bytes stay as they are. */
  encoding: string;
  /** Both LF and CRLF in the file: saving makes them all the same. */
  mixedEol: boolean;
  /** Why the file cannot be edited ("" = editable). */
  readOnly: string;
  /** Changed on disk while there are unsaved edits, or a save was refused for it. */
  conflict: boolean;
  deleted: boolean;
  outside: boolean;
  saving: boolean;
  saveError: string;
  recovered: Draft | null;
  md: MdView;
}

export interface CommitTab {
  hash: string;
  /** The repository it belongs to, whichever one is selected later. */
  repo: string;
  label: string;
  /** The file last chosen in it. */
  file?: string;
}

/** A view of its own in a tab, instead of a file. */
export type SpecialView = { kind: 'review'; session: string; path: string; agent: string } | { kind: 'history'; path: string } | { kind: 'graph' } | { kind: 'pr'; number: number; title: string; repo: string };

const viewId = (v: SpecialView) => (v.kind === 'review' ? `review:${v.session}:${v.path}` : v.kind === 'history' ? `history:${v.path}` : v.kind === 'pr' ? `pr:${v.repo}:${v.number}` : v.kind);

export interface Tab {
  id: string;
  /** "" when it shows no file: a pane, a commit or a special view. */
  path: string;
  diff?: DiffSource;
  /** An agent console or the orchestrator instead of a file (AgentPanes.tsx). */
  pane?: string;
  /** A commit's details instead of a file (CommitView.tsx). */
  commit?: CommitTab;
  view?: SpecialView;
  preview: boolean;
}

/** The id of the orchestrator's pane; a console's pane has its session id. */
export const ORCHESTRATOR_PANE = 'orchestrator';

export interface Group {
  id: string;
  tabs: Tab[];
  active: string | null;
}

export interface SearchState {
  query: string;
  replace: string;
  matchCase: boolean;
  wholeWord: boolean;
  regex: boolean;
  include: string;
  exclude: string;
}

export interface EditorPrefs {
  fontSize: number;
  tabSize: number;
  minimap: boolean;
  wordWrap: boolean;
  formatOnSave: boolean;
  autoSave: 'off' | 'afterDelay' | 'onFocusChange';
  stickyScroll: boolean;
  bracketColors: boolean;
  renderWhitespace: boolean;
  /** Ghost text from the runner: never, on Alt+\\, or while typing. */
  aiInline: 'off' | 'manual' | 'auto';
  /** Claude consoles linked to this editor see the selected text. */
  shareSelection: boolean;
}

export const DEFAULT_EDITOR_PREFS: EditorPrefs = {
  fontSize: 13,
  tabSize: 2,
  minimap: true,
  wordWrap: false,
  formatOnSave: false,
  autoSave: 'off',
  stickyScroll: true,
  bracketColors: true,
  renderWhitespace: false,
  aiInline: 'off',
  shareSelection: true,
};

export type TreeSlot = TreeEntry[] | 'loading' | { error: string };

/** A row being typed into the tree: a new file or folder, or a rename. */
export interface TreeInput {
  kind: 'file' | 'folder' | 'rename';
  dir: string;
  path?: string;
  value: string;
}

/** A question answered by the hidden runner, right in the Ask view. */
export interface QuickTurn {
  id: number;
  question: string;
  contextLabel: string;
  jobId: string;
  status: 'running' | 'done' | 'failed';
  text: string;
  error: string;
}

export interface AgentContextInput {
  label: string;
  rel: string;
  language: string;
  code: string;
  startLine: number;
  endLine: number;
}

export type RemoteRun = <T>(fn: (c: Confirm) => Promise<T>) => Promise<T | null>;

export interface GitMark {
  letter: string;
  tone: string;
}

const STATUS_TONE: Record<string, string> = { modified: 'modified', typechange: 'modified', added: 'added', untracked: 'added', renamed: 'renamed', deleted: 'deleted', conflict: 'deleted' };
const STATUS_LETTERS: Record<string, string> = { modified: 'M', typechange: 'T', added: 'A', untracked: 'U', renamed: 'R', deleted: 'D', conflict: '!' };

const newDoc = (path: string, md: MdView): Doc => ({
  path,
  kind: 'text',
  status: 'loading',
  error: '',
  model: null,
  savedAlt: 0,
  dirty: false,
  mtime: '',
  size: 0,
  bom: false,
  encoding: '',
  mixedEol: false,
  readOnly: '',
  conflict: false,
  deleted: false,
  outside: false,
  saving: false,
  saveError: '',
  recovered: null,
  md,
});

const tabId = (path: string, diff?: DiffSource) => (diff ? `diff:${diff}:${path}` : path);
const paneTab = (pane: string): Tab => ({ id: `pane:${pane}`, path: '', pane, preview: false });
const readJSON = <T>(key: string, fallback: T): T => {
  try {
    const raw = prefs.get(key);
    return raw ? { ...fallback, ...(JSON.parse(raw) as T) } : fallback;
  } catch {
    return fallback;
  }
};

interface Persisted {
  groups: { tabs: { path: string; preview: boolean; pane?: string }[]; active: string | null }[];
  activeGroup: number;
  expanded: string[];
  side: SideView;
  sideOpen: boolean;
  sideW: number;
  panelOpen: boolean;
  panelH: number;
  panelView: PanelView;
  showExcluded: boolean;
  search: SearchState;
  recent: string[];
  terminals: string[];
  activeTerminal: string;
  md: Record<string, MdView>;
  agentTarget: string;
  rightOpen: boolean;
  rightW: number;
  rightPanes: string[];
  rightActive: string;
}

const DEFAULT_SEARCH: SearchState = { query: '', replace: '', matchCase: false, wholeWord: false, regex: false, include: '', exclude: '' };

const treeSlots = limiter(3);
const ignoredSlots = limiter(1);

const workspaces = new Map<string, Workspace>();

export class Workspace {
  readonly mid: string;
  readonly root: string;
  readonly pid: string;
  name: string;

  version = 0;
  cursorVersion = 0;
  private listeners = new Set<() => void>();
  private cursorListeners = new Set<() => void>();

  docs = new Map<string, Doc>();
  groups: Group[] = [{ id: 'g1', tabs: [], active: null }];
  activeGroup = 0;
  private groupSeq = 1;

  side: SideView = 'files';
  sideOpen = true;
  sideW = 280;
  panelOpen = false;
  panelH = 260;
  panelView: PanelView = 'terminal';
  showExcluded = true;
  search: SearchState = DEFAULT_SEARCH;
  recent: string[] = [];
  terminals: string[] = [];
  activeTerminal = '';
  private mdViews: Record<string, MdView> = {};
  editorPrefs: EditorPrefs;

  items = new Map<string, TreeSlot>();
  expanded = new Set<string>();
  truncated = new Set<string>();
  ignored = new Set<string>();
  selected: string | null = null;
  input: TreeInput | null = null;

  git: GitStatus | null = null;
  gitVersion = 0;
  review: ReviewSummary | null = null;
  bookmarks: { path: string; line: number }[] = [];
  /** Moves when a pull request changed from here, so that the list follows. */
  pullsTick = 0;
  tasks: ProjectTask[] = [];
  /** Task id → its last run. */
  taskRuns = new Map<string, { jobId: string; state: string; error: string }>();
  taskSelected = '';
  private reviewMarks = new Map<string, string[]>();
  private gitFiles = new Map<string, GitMark>();
  private gitDirs = new Map<string, string>();
  private gitTimer = 0;

  output: string[] = [];

  /** Asked to save or discard before closing a dirty tab. */
  closing: { group: number; tab: Tab } | null = null;

  /** The editor of each group, for commands and formatting. */
  editors = new Map<string, monaco.editor.IStandaloneCodeEditor>();
  /** A line to show once the tab's editor has the model. */
  reveal: { tabId: string; group: string; line: number; col?: number; endCol?: number } | null = null;
  /** Bumped to move focus to the active editor. */
  focusTick = 0;
  /** The last request an editor answered: one that appears later has no claim on the focus. */
  focusDone = 0;
  /** Bumped to scroll the tree to the selected row. */
  revealTick = 0;
  cursor = { line: 1, col: 1, selected: 0 };

  run: RemoteRun = (fn) => fn({});
  lsp: LspHub;
  debug: Debugger;
  /** Ghost text requests in flight, for the status bar. */
  aiBusy = 0;
  /** Where questions from the editor go ("quick", "planner", "console:x", "group:x", "card:id"). */
  agentTarget = '';
  quickTurns: QuickTurn[] = [];
  private quickSeq = 1;
  /** What the editor's own menu needs from the card around it. */
  /** The right side bar: panes by id, and which one shows. */
  rightOpen = false;
  rightW = 460;
  rightPanes: string[] = [];
  rightActive = '';
  /** Which pane should take the keyboard. */
  paneFocus = { id: '', tick: 0 };
  /** The console asked to be closed: "stop the agent?". */
  closingPane = '';
  /** The link to Claude consoles on the board that shows this editor. */
  readonly ide = new IdeLink(this);
  actions: { ask?: (prompt?: string, ctx?: AgentContextInput) => void; aiEdit?: () => void; palette?: () => void; mention?: () => void; openWeb?: (url: string) => void; pick?: (placeholder: string, items: { label: string; detail?: string; run: () => void }[]) => void; addToBoard?: (path: string) => void } = {};
  private filesCache: { at: number; list: FileListing } | null = null;
  private persistTimer = 0;
  private watch: { id: string; off: () => void; key: string; renew: number } | null = null;
  private watchTimer = 0;
  private watchSeq = 0;
  private lspTimer = 0;
  private draftTimers = new Map<string, number>();
  private autoSaveTimers = new Map<string, number>();
  private mounted = false;

  static get(project: Project): Workspace {
    let w = workspaces.get(project.id);
    if (!w) {
      w = new Workspace(project);
      workspaces.set(project.id, w);
    }
    w.name = project.name;
    return w;
  }

  private constructor(project: Project) {
    this.pid = project.id;
    this.mid = project.machineId;
    this.root = project.path.replace(/\/+$/, '') || '/';
    this.name = project.name;
    this.editorPrefs = readJSON('wb.editor', DEFAULT_EDITOR_PREFS);
    this.bookmarks = readJSON(`wb.bookmarks.${this.pid}`, []);
    this.lsp = new LspHub({
      projectId: this.pid,
      root: this.root,
      ensureModel: (p) => this.ensureModel(p),
      log: (l) => this.log(l),
      changed: () => this.changed(),
    });
    this.debug = new Debugger(this);
    monaco.editor.onDidChangeMarkers(() => this.changed());
    let reviewRev = studioStore.get().reviewRev[this.pid] ?? 0;
    studioStore.subscribe(() => {
      const rev = studioStore.get().reviewRev[this.pid] ?? 0;
      if (rev === reviewRev) return;
      reviewRev = rev;
      void this.refreshReview();
    });
    studioStore.onReviewOverlap.add((e) => {
      if (e.projectId === this.pid) toast.info(`${e.agents.join(' and ')} both changed ${e.path}. Check it in Review.`);
    });
    void this.refreshReview();
    let gitRepo = this.gitTarget.repo;
    studioStore.subscribe(() => {
      if (gitRepo === this.gitTarget.repo) return;
      gitRepo = this.gitTarget.repo;
      this.git = null;
      void this.refreshGit();
    });
    const saved = readJSON<Partial<Persisted>>(`wb.${this.pid}`, {});
    this.side = saved.side ?? 'files';
    this.sideOpen = saved.sideOpen ?? true;
    this.sideW = saved.sideW ?? 280;
    this.panelOpen = saved.panelOpen ?? false;
    this.panelH = saved.panelH ?? 260;
    this.panelView = saved.panelView ?? 'terminal';
    this.rightOpen = saved.rightOpen ?? false;
    this.rightW = saved.rightW ?? 460;
    this.rightPanes = saved.rightPanes ?? [];
    this.rightActive = saved.rightActive ?? '';
    this.showExcluded = saved.showExcluded ?? true;
    this.search = { ...DEFAULT_SEARCH, ...saved.search };
    this.recent = saved.recent ?? [];
    this.terminals = saved.terminals ?? [];
    this.activeTerminal = saved.activeTerminal ?? '';
    this.mdViews = saved.md ?? {};
    this.agentTarget = saved.agentTarget ?? '';
    this.expanded = new Set(saved.expanded ?? [this.root]);
    this.expanded.add(this.root);
    const restored = (saved.groups ?? []).filter((g) => g.tabs?.length).slice(0, MAX_GROUPS);
    if (restored.length) {
      this.groups = restored.map((g) => ({
        id: `g${this.groupSeq++}`,
        tabs: g.tabs.map((t) => (t.pane ? paneTab(t.pane) : { id: tabId(t.path), path: t.path, preview: t.preview })),
        active: g.active,
      }));
      this.activeGroup = Math.min(saved.activeGroup ?? 0, this.groups.length - 1);
      for (const g of this.groups) for (const t of g.tabs) if (!t.pane) this.ensureDoc(t.path);
    }
  }

  // ---- subscriptions -----------------------------------------------------------------

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  };
  subscribeCursor = (fn: () => void) => {
    this.cursorListeners.add(fn);
    return () => {
      this.cursorListeners.delete(fn);
    };
  };
  getVersion = () => this.version;
  getCursorVersion = () => this.cursorVersion;

  changed() {
    this.version++;
    for (const fn of this.listeners) fn();
    window.clearTimeout(this.persistTimer);
    this.persistTimer = window.setTimeout(() => this.persist(), 400);
  }

  setCursor(line: number, col: number, selected: number) {
    this.cursor = { line, col, selected };
    this.cursorVersion++;
    for (const fn of this.cursorListeners) fn();
  }

  private persist() {
    const p: Persisted = {
      groups: this.groups.map((g) => ({
        tabs: g.tabs.filter((t) => !t.diff && !t.commit && !t.view).map((t) => ({ path: t.path, preview: t.preview, pane: t.pane })),
        active: g.active?.startsWith('diff:') ? null : g.active,
      })),
      activeGroup: this.activeGroup,
      expanded: [...this.expanded],
      side: this.side,
      sideOpen: this.sideOpen,
      sideW: this.sideW,
      panelOpen: this.panelOpen,
      panelH: this.panelH,
      panelView: this.panelView,
      rightOpen: this.rightOpen,
      rightW: this.rightW,
      rightPanes: this.rightPanes,
      rightActive: this.rightActive,
      showExcluded: this.showExcluded,
      search: this.search,
      recent: this.recent,
      terminals: this.terminals,
      activeTerminal: this.activeTerminal,
      md: this.mdViews,
      agentTarget: this.agentTarget,
    };
    prefs.set(`wb.${this.pid}`, JSON.stringify(p));
  }

  log(line: string) {
    this.output = [...this.output.slice(-999), line];
    this.changed();
  }

  // ---- mount -------------------------------------------------------------------------

  /** The workbench opened: start what only runs while it is visible. */
  mount() {
    this.mounted = true;
    window.clearTimeout(this.lspTimer);
    if (!this.lsp.servers.length) void this.lsp.start();
    else this.lsp.resume();
    for (const d of this.expanded) void this.loadDir(d);
    void this.refreshGit();
    this.syncWatch();
  }

  /** The workbench closed: tabs stay, polling stops, servers stop after a while. */
  unmount() {
    this.mounted = false;
    this.persist();
    this.stopWatch();
    window.clearTimeout(this.lspTimer);
    this.lspTimer = window.setTimeout(() => {
      if (!this.mounted) this.lsp.dispose();
    }, LSP_IDLE_MS);
  }

  /** Ask the hidden runner about the code; the answer streams into the Ask view. */
  /** `files` go along with the question, without showing in the conversation. */
  async askQuick(question: string, ctx: AgentContextInput | null, files: { path: string; text: string }[] = []) {
    const turn: QuickTurn = { id: this.quickSeq++, question, contextLabel: ctx?.label ?? '', jobId: '', status: 'running', text: '', error: '' };
    this.quickTurns = [...this.quickTurns.slice(-30), turn];
    this.changed();
    try {
      const attached = files.map((f) => `\n\nFile ${f.path}:\n\`\`\`\n${f.text}\n\`\`\``).join('');
      const j = await filesApi.aiAsk(this.pid, { question: question + attached, path: ctx?.rel ?? '', language: ctx?.language ?? '', selection: ctx?.code ?? '', startLine: ctx?.startLine ?? 0, endLine: ctx?.endLine ?? 0 });
      turn.jobId = j.id;
      this.changed();
      const done = await waitJob(j.id, 6 * 60_000);
      const text = (done.result as { text?: unknown } | null)?.text;
      if (done.state === 'done' && typeof text === 'string') {
        turn.text = text;
        turn.status = 'done';
      } else {
        turn.status = 'failed';
        turn.error = done.error || 'No answer came back.';
      }
    } catch (err) {
      turn.status = 'failed';
      turn.error = errorMessage(err);
    }
    this.changed();
  }

  clearQuick() {
    this.quickTurns = [];
    this.changed();
  }

  setAiBusy(delta: number) {
    this.aiBusy = Math.max(0, this.aiBusy + delta);
    this.changed();
  }

  setPrefs(patch: Partial<EditorPrefs>) {
    this.editorPrefs = { ...this.editorPrefs, ...patch };
    prefs.set('wb.editor', JSON.stringify(this.editorPrefs));
    this.changed();
  }

  // ---- paths -------------------------------------------------------------------------

  rel(p: string): string {
    return p.startsWith(`${this.root}/`) ? p.slice(this.root.length + 1) : p;
  }
  abs(rel: string): string {
    return this.root === '/' ? `/${rel}` : `${this.root}/${rel}`;
  }
  inside(p: string): boolean {
    return p === this.root || p.startsWith(`${this.root}/`);
  }

  // ---- tree --------------------------------------------------------------------------

  async loadDir(dir: string) {
    if (!this.items.has(dir)) {
      this.items.set(dir, 'loading');
      this.changed();
    }
    try {
      const t = await treeSlots(() => fsApi.tree(this.mid, dir, true));
      const entries = (t.entries ?? [])
        .filter((e) => e.name !== '.git' && e.name !== '.nextboard-trash')
        .sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
      this.items.set(dir, entries);
      if (t.truncated) this.truncated.add(dir);
      else this.truncated.delete(dir);
      this.changed();
      void this.loadIgnored(dir, entries);
    } catch (err) {
      this.items.set(dir, { error: errorMessage(err) });
      this.changed();
    }
  }

  private async loadIgnored(dir: string, entries: TreeEntry[]) {
    if (!this.inside(dir) || !entries.length) return;
    const paths = entries.map((e) => this.rel(`${dir === '/' ? '' : dir}/${e.name}`));
    try {
      const r = await ignoredSlots(() => filesApi.ignored(this.pid, paths));
      for (const p of paths) this.ignored.delete(this.abs(p));
      for (const p of r.items ?? []) this.ignored.add(this.abs(p.replace(/\/$/, '')));
      this.changed();
    } catch {
      /* an older hub: nothing is dimmed */
    }
  }

  toggleDir(dir: string, open?: boolean) {
    const next = open ?? !this.expanded.has(dir);
    if (next) {
      this.expanded.add(dir);
      void this.loadDir(dir);
    } else {
      this.expanded.delete(dir);
    }
    this.changed();
    this.syncWatch();
  }

  collapseAll() {
    this.expanded = new Set([this.root]);
    this.changed();
    this.syncWatch();
  }

  refreshTree() {
    this.filesCache = null;
    for (const d of this.expanded) void this.loadDir(d);
    void this.refreshGit();
  }

  /** Open the folders above a file and scroll to it. */
  revealInTree(path: string) {
    this.side = 'files';
    this.sideOpen = true;
    this.selected = path;
    if (this.inside(path)) {
      let d = dirOf(path);
      while (this.inside(d) && d !== this.root) {
        if (!this.expanded.has(d)) {
          this.expanded.add(d);
          void this.loadDir(d);
        }
        d = dirOf(d);
      }
    }
    this.revealTick++;
    this.changed();
    this.syncWatch();
  }

  // ---- git ---------------------------------------------------------------------------

  /** The repository the Git views work on (the picker of the Git panel and of source control). */
  get gitTarget(): GitTarget {
    return studioStore.gitTarget(this.pid);
  }

  /** Refresh soon, once: saves and file events come in bursts. */
  gitSoon() {
    window.clearTimeout(this.gitTimer);
    this.gitTimer = window.setTimeout(() => void this.refreshGit(), 700);
  }

  async refreshGit() {
    try {
      await studioStore.loadGitRepos(this.pid);
      const target = this.gitTarget;
      const st = await gitApi.status(target);
      // the picker moved on: its own refresh is under way
      if (target.repo !== this.gitTarget.repo) return;
      this.git = st;
      this.gitFiles.clear();
      this.gitDirs.clear();
      const base = st.root?.replace(/\/+$/, '') || this.root;
      for (const f of st.files ?? []) {
        const tone = STATUS_TONE[f.status] ?? 'modified';
        const abs = `${base}/${f.path}`;
        this.gitFiles.set(abs, { letter: STATUS_LETTERS[f.status] ?? '•', tone });
        for (let d = dirOf(abs); d.length > 1 && this.inside(d); d = dirOf(d)) {
          // a conflict or deletion outranks a modification in the folder's colour
          if (!this.gitDirs.has(d) || tone === 'deleted') this.gitDirs.set(d, tone);
          if (d === this.root) break;
        }
      }
    } catch {
      this.git = null;
      this.gitFiles.clear();
      this.gitDirs.clear();
    }
    this.gitVersion++;
    this.changed();
  }

  gitMark(path: string): GitMark | undefined {
    return this.gitFiles.get(path);
  }

  // ---- review of what agents changed (ReviewPanel.tsx) ----------------------------------

  async refreshReview() {
    try {
      this.review = await reviewApi.list(this.pid);
    } catch {
      this.review = { supported: false, agents: [] };
    }
    this.reviewMarks.clear();
    for (const a of this.review.agents) {
      for (const f of a.files) {
        const abs = this.abs(f.path);
        this.reviewMarks.set(abs, [...(this.reviewMarks.get(abs) ?? []), a.name]);
      }
    }
    this.changed();
  }

  /** The agents with a change to this file nobody has looked at yet. */
  reviewMark(path: string): string[] | undefined {
    return this.reviewMarks.get(path);
  }

  get reviewCount(): number {
    return this.reviewMarks.size;
  }

  // ---- local history (LocalHistoryView.tsx) -----------------------------------------------

  /** What a file holds for the owner now: the editor's text when it is open, else the disk's. */
  async currentText(path: string): Promise<string> {
    const model = this.docs.get(path)?.model;
    if (model) return model.getValue();
    try {
      const r = await api.fsRead(this.mid, path, MAX_VIEW);
      return r.content.startsWith('\ufeff') ? r.content.slice(1) : r.content;
    } catch {
      return ''; // it was deleted
    }
  }

  /** Bring back an earlier text: into the editor when the file is open, where Ctrl+Z undoes it. */
  async restoreText(path: string, text: string) {
    const model = this.docs.get(path)?.model;
    if (model) {
      // one step of its own, or Ctrl+Z also takes back what was typed before
      model.pushStackElement();
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text }], () => null);
      model.pushStackElement();
      this.open(path, { pin: true });
      toast.success('Restored in the editor. Save to keep it.');
      return;
    }
    await fsApi.write(this.mid, { path, content: text, projectId: this.pid });
    this.open(path, { pin: true });
    toast.success('Restored.');
  }
  gitDirTone(path: string): string | undefined {
    return this.gitDirs.get(path);
  }
  /** Path relative to the repository (git API), or null outside it. */
  repoRel(path: string): string | null {
    const base = this.git?.root?.replace(/\/+$/, '');
    if (!this.git?.isRepo || !base) return null;
    return path.startsWith(`${base}/`) ? path.slice(base.length + 1) : null;
  }

  // ---- the file list -----------------------------------------------------------------

  /** Every project file (ignored ones left out), cached briefly. */
  async fileList(force = false): Promise<FileListing> {
    if (!force && this.filesCache && Date.now() - this.filesCache.at < 30_000) return this.filesCache.list;
    const list = await filesApi.list(this.pid);
    this.filesCache = { at: Date.now(), list };
    return list;
  }

  // ---- watching ----------------------------------------------------------------------

  /** Watch the open folders and files; the hub stats them every few seconds. */
  syncWatch() {
    window.clearTimeout(this.watchTimer);
    this.watchTimer = window.setTimeout(() => void this.startWatch(), 500);
  }

  private async startWatch() {
    if (!this.mounted) return;
    const files = [...this.docs.values()].filter((d) => d.status === 'ready').map((d) => d.path);
    const paths = [...new Set([...files, ...this.expanded])].slice(0, 200);
    const key = paths.slice().sort().join('\n');
    if (this.watch?.key === key) return;
    this.stopWatch();
    if (!paths.length) return;
    const seq = ++this.watchSeq;
    try {
      const w = await fsApi.watch(this.mid, paths, 60);
      // closed meanwhile, or a newer set of paths is already on its way
      if (!this.mounted || seq !== this.watchSeq) {
        void fsApi.unwatch(w.watchId).catch(() => undefined);
        return;
      }
      const off = studioStore.watchFs(w.watchId, (ev) => {
        for (const c of ev.changes ?? []) this.onDiskChange(c.path, c.exists, c.mtime);
      });
      // a renewal buys 30 s: well before that, and a lost watch (hub restart) starts over
      const renew = window.setInterval(() => {
        void fsApi.renewWatch(w.watchId).catch(() => {
          if (this.watch?.id !== w.watchId) return;
          this.stopWatch();
          this.syncWatch();
        });
      }, 15_000);
      this.watch = { id: w.watchId, off: () => off(), key, renew };
    } catch {
      /* no watch: the tree refreshes by hand, a save still detects conflicts */
    }
  }

  private stopWatch() {
    const w = this.watch;
    if (!w) return;
    this.watch = null;
    w.off();
    window.clearInterval(w.renew);
    void fsApi.unwatch(w.id).catch(() => undefined);
  }

  private onDiskChange(path: string, exists: boolean, mtime: string | undefined) {
    if (this.expanded.has(path)) {
      this.filesCache = null;
      void this.loadDir(path);
      this.gitSoon();
    }
    const d = this.docs.get(path);
    if (!d || d.status !== 'ready' || d.saving) return;
    if (!exists) {
      d.deleted = true;
      this.changed();
      return;
    }
    if (!mtime || mtime === d.mtime) return;
    d.deleted = false;
    this.gitSoon();
    if (d.dirty) {
      d.conflict = true;
      this.changed();
    } else {
      void this.reload(path);
    }
  }

  // ---- documents ---------------------------------------------------------------------

  private ensureDoc(path: string): Doc {
    let d = this.docs.get(path);
    if (!d) {
      d = newDoc(path, this.mdViews[path] ?? 'reader');
      this.docs.set(path, d);
      void this.load(d);
    }
    return d;
  }

  /** For language servers: the model of a file, without opening a tab. */
  async ensureModel(path: string): Promise<monaco.editor.ITextModel | null> {
    const existing = monaco.editor.getModel(monaco.Uri.file(path));
    if (existing) return existing;
    const d = this.ensureDoc(path);
    await this.loaded(d);
    return d.model;
  }

  private loading = new Map<string, Promise<void>>();

  private loaded(d: Doc): Promise<void> {
    return this.loading.get(d.path) ?? Promise.resolve();
  }

  private load(d: Doc): Promise<void> {
    const p = this.loadInner(d).finally(() => this.loading.delete(d.path));
    this.loading.set(d.path, p);
    return p;
  }

  private async loadInner(d: Doc) {
    const ext = extOf(d.path);
    try {
      const st = await api.fsStat(this.mid, d.path);
      d.size = st.size;
      d.mtime = st.mtime;
      if (st.exists === false) throw new Error('This file no longer exists.');
      if (IMAGE_EXT.has(ext) || MEDIA_EXT.has(ext)) {
        d.kind = IMAGE_EXT.has(ext) ? 'image' : 'media';
        d.status = 'ready';
        d.readOnly = 'Preview';
        this.changed();
        return;
      }
      if (st.size > MAX_VIEW) {
        d.kind = 'toolarge';
        d.status = 'ready';
        d.readOnly = `This file is ${Math.round(st.size / (1 << 20))} MB; files over 10 MB are not opened.`;
        this.changed();
        return;
      }
      const r = await api.fsRead(this.mid, d.path, MAX_VIEW + 1);
      d.size = r.size;
      d.mtime = r.mtime;
      let content = r.content;
      if (r.binary) {
        const legacy = await this.decodeLegacy(d.path);
        if (legacy === null) {
          d.kind = 'binary';
          d.status = 'ready';
          d.readOnly = 'Binary file: not opened.';
          this.changed();
          return;
        }
        content = legacy.text;
        d.encoding = legacy.encoding;
      }
      d.bom = !d.encoding && content.startsWith('\uFEFF');
      const text = d.bom ? content.slice(1) : content;
      d.mixedEol = /\r\n/.test(text) && /(^|[^\r])\n/.test(text);
      const uri = monaco.Uri.file(d.path);
      const model = monaco.editor.getModel(uri) ?? monaco.editor.createModel(text, languageFor(d.path), uri);
      if (model.getValue() !== text.replace(/\r\n|\r|\n/g, model.getEOL())) model.setValue(text);
      d.model = model;
      d.savedAlt = model.getAlternativeVersionId();
      d.kind = 'text';
      d.status = 'ready';
      d.readOnly = !this.inside(d.path)
        ? 'Outside the project: read-only.'
        : d.encoding
          ? `Not UTF-8 (shown as ${d.encoding}): read-only, so that its bytes stay as they are.`
          : st.size > MAX_EDIT
            ? `Files over 1 MB open read-only (this one is ${Math.round(st.size / 1024)} KB).`
            : '';
      model.onDidChangeContent(() => this.onModelChange(d));
      this.lsp.attach(model);
      const draft = await drafts.get(this.mid, d.path);
      if (draft && draft.content !== model.getValue()) {
        // The same base as on disk: the unsaved edits simply come back, like a hot exit.
        // Otherwise the file moved on and the user has to choose.
        if (draft.baseMtime === d.mtime) {
          model.setValue(draft.content);
        } else {
          d.recovered = draft;
        }
      } else if (draft) {
        void drafts.remove(this.mid, d.path);
      }
      this.updateDirty(d);
      this.changed();
      this.syncWatch();
    } catch (err) {
      d.status = 'error';
      d.error = errorMessage(err);
      this.changed();
    }
  }

  /** The bytes of a file the hub would not hand over as UTF-8; null for real binaries. */
  private async decodeLegacy(path: string, encoding?: string): Promise<{ text: string; encoding: string } | null> {
    const res = await fetch(fsRawUrl(this.mid, path));
    if (!res.ok) throw new Error(`Could not read the file (${res.status}).`);
    const bytes = new Uint8Array(await res.arrayBuffer());
    let enc = encoding;
    if (!enc) {
      if (bytes[0] === 0xff && bytes[1] === 0xfe) enc = 'utf-16le';
      else if (bytes[0] === 0xfe && bytes[1] === 0xff) enc = 'utf-16be';
      else if (bytes.subarray(0, 8192).includes(0)) return null;
      else enc = 'windows-1252';
    }
    return { text: new TextDecoder(enc).decode(bytes), encoding: enc };
  }

  /** "Reopen with Encoding": the file stays read-only, only the view changes. */
  async reopenWithEncoding(path: string, encoding: string) {
    const d = this.docs.get(path);
    if (!d?.model || !d.encoding) return;
    try {
      const r = await this.decodeLegacy(path, encoding);
      if (!r) return;
      d.encoding = r.encoding;
      d.readOnly = `Not UTF-8 (shown as ${r.encoding}): read-only, so that its bytes stay as they are.`;
      d.model.setValue(r.text);
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      this.changed();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  private onModelChange(d: Doc) {
    const was = d.dirty;
    this.updateDirty(d);
    if (d.dirty) {
      // typing keeps the tab
      for (const g of this.groups) {
        for (const t of g.tabs) if (t.path === d.path && t.preview && !t.diff) t.preview = false;
      }
      this.scheduleDraft(d);
      if (this.editorPrefs.autoSave === 'afterDelay' && !d.readOnly) this.scheduleAutoSave(d);
    }
    if (d.dirty !== was || d.dirty) this.changed();
  }

  private updateDirty(d: Doc) {
    if (d.model) d.dirty = d.model.getAlternativeVersionId() !== d.savedAlt;
  }

  private scheduleDraft(d: Doc) {
    window.clearTimeout(this.draftTimers.get(d.path));
    this.draftTimers.set(
      d.path,
      window.setTimeout(() => this.writeDraft(d), 500),
    );
  }

  private writeDraft(d: Doc) {
    if (!d.model || !d.dirty) return;
    void drafts.put(this.mid, d.path, { content: d.model.getValue(), baseMtime: d.mtime, at: Date.now() });
  }

  /** Write every pending draft now (the workbench is closing). */
  flushDrafts() {
    for (const d of this.docs.values()) if (d.dirty) this.writeDraft(d);
  }

  private scheduleAutoSave(d: Doc) {
    window.clearTimeout(this.autoSaveTimers.get(d.path));
    this.autoSaveTimers.set(
      d.path,
      window.setTimeout(() => {
        if (d.dirty && !d.conflict) void this.save(d.path);
      }, 1000),
    );
  }

  /** Focus left the editor or the window (auto save "on focus change"). */
  focusChanged() {
    if (this.editorPrefs.autoSave !== 'onFocusChange') return;
    for (const d of this.docs.values()) if (d.dirty && !d.readOnly && !d.conflict) void this.save(d.path);
  }

  async reload(path: string) {
    const d = this.docs.get(path);
    if (!d?.model) {
      if (d) {
        d.status = 'loading';
        this.changed();
        await this.load(d);
      }
      return;
    }
    try {
      const r = await api.fsRead(this.mid, path, MAX_VIEW + 1);
      const legacy = r.binary ? await this.decodeLegacy(path, d.encoding || undefined) : null;
      if (r.binary && !legacy) return;
      const text = legacy ? legacy.text : r.content.startsWith('\uFEFF') ? r.content.slice(1) : r.content;
      this.replaceAll(d.model, text);
      d.mtime = r.mtime;
      d.size = r.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.conflict = false;
      d.deleted = false;
      d.dirty = false;
      void drafts.remove(this.mid, path);
      this.changed();
    } catch (err) {
      toast.error(`Could not reload: ${errorMessage(err)}`);
    }
  }

  /** Replace the text as one undoable step, so Undo brings back what was there. */
  private replaceAll(model: monaco.editor.ITextModel, text: string) {
    if (model.getValue() === text.replace(/\r\n|\r|\n/g, model.getEOL())) return;
    model.pushStackElement();
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text }], () => null);
    model.pushStackElement();
  }

  async save(path: string, opts: { force?: boolean; outside?: boolean } = {}): Promise<boolean> {
    const d = this.docs.get(path);
    if (!d?.model || d.readOnly || d.status !== 'ready') return false;
    window.clearTimeout(this.draftTimers.get(path));
    window.clearTimeout(this.autoSaveTimers.get(path));
    d.saving = true;
    d.saveError = '';
    this.changed();
    try {
      if (this.editorPrefs.formatOnSave) await this.formatDoc(d);
      let ifMtime = d.mtime || undefined;
      if (opts.force) ifMtime = (await api.fsStat(this.mid, path)).mtime || undefined;
      const body = { path, content: (d.bom ? '﻿' : '') + d.model.getValue(), ifMtime, projectId: this.pid, allowOutside: opts.outside || undefined };
      const st = await this.run((c) => fsApi.write(this.mid, { ...body, ...c }));
      if (!st) {
        d.saving = false;
        this.changed();
        return false;
      }
      d.mtime = st.mtime;
      d.size = st.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      d.conflict = false;
      d.deleted = false;
      d.outside = false;
      d.saving = false;
      d.recovered = null;
      void drafts.remove(this.mid, path);
      this.lsp.saved(d.model);
      this.gitSoon();
      this.changed();
      void this.runOnSave(d);
      return true;
    } catch (err) {
      d.saving = false;
      if (isCode(err, 'mtime_mismatch') || (err instanceof ApiError && err.status === 409)) d.conflict = true;
      else if (isCode(err, 'outside_project')) d.outside = true;
      else if (isCode(err, 'no_sftp')) d.readOnly = 'This machine has no SFTP, so files can only be read.';
      else d.saveError = errorMessage(err);
      this.changed();
      return false;
    }
  }

  async saveAll() {
    for (const d of this.docs.values()) if (d.dirty && !d.readOnly && !d.conflict) await this.save(d.path);
  }

  private async formatDoc(d: Doc) {
    for (const ed of this.editors.values()) {
      if (ed.getModel() === d.model) {
        await ed.getAction('editor.action.formatDocument')?.run().catch(() => undefined);
        return;
      }
    }
  }

  private async runOnSave(d: Doc) {
    if (!this.inside(d.path) || !store.getState().settings?.editor?.onSave?.length) return;
    try {
      const r = await filesApi.onSave(this.pid, this.rel(d.path));
      for (const run of r.runs ?? []) {
        this.log(`$ ${run.command}${run.output ? `\n${run.output.trimEnd()}` : ''}`);
        if (run.exitCode !== 0 || run.timedOut) {
          toast.notify('error', 'A command on save failed', `${run.command}: ${run.timedOut ? 'stopped after 60 s' : run.output.trim().slice(-300) || `exit ${run.exitCode}`}`);
        }
      }
      if (!r.runs?.length || !d.model) return;
      // A formatter may have changed the file: take it in as an undoable edit.
      const fresh = await api.fsRead(this.mid, d.path, MAX_VIEW + 1);
      if (fresh.mtime === d.mtime || d.dirty) return;
      this.replaceAll(d.model, fresh.content.startsWith('﻿') ? fresh.content.slice(1) : fresh.content);
      d.mtime = fresh.mtime;
      d.size = fresh.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      this.changed();
    } catch (err) {
      this.log(`Commands on save: ${errorMessage(err)}`);
    }
  }

  restoreDraft(path: string) {
    const d = this.docs.get(path);
    if (!d?.recovered || !d.model) return;
    d.model.setValue(d.recovered.content);
    d.recovered = null;
    this.updateDirty(d);
    this.changed();
  }

  dropDraft(path: string) {
    const d = this.docs.get(path);
    if (!d) return;
    void drafts.remove(this.mid, path);
    d.recovered = null;
    this.changed();
  }

  setMdView(path: string, v: MdView) {
    const d = this.docs.get(path);
    if (d) d.md = v;
    this.mdViews[path] = v;
    this.changed();
  }

  private releaseDoc(path: string) {
    for (const g of this.groups) if (g.tabs.some((t) => t.path === path)) return;
    const d = this.docs.get(path);
    if (!d) return;
    if (d.model) {
      this.lsp.detach(d.model);
      if (d.dirty) void drafts.remove(this.mid, path);
      d.model.dispose();
    }
    this.docs.delete(path);
    this.syncWatch();
  }

  // ---- tabs --------------------------------------------------------------------------

  get group(): Group {
    return this.groups[this.activeGroup] ?? this.groups[0];
  }

  get activeTab(): Tab | null {
    const g = this.group;
    return g.tabs.find((t) => t.id === g.active) ?? null;
  }

  get activeDoc(): Doc | null {
    const t = this.activeTab;
    return t ? (this.docs.get(t.path) ?? null) : null;
  }

  activeEditor(): monaco.editor.IStandaloneCodeEditor | null {
    return this.editors.get(this.group.id) ?? null;
  }

  private groupForSide(from: number): number {
    if (from + 1 < this.groups.length) return from + 1;
    if (this.groups.length >= MAX_GROUPS) return from;
    this.groups = [...this.groups, { id: `g${++this.groupSeq}`, tabs: [], active: null }];
    return this.groups.length - 1;
  }

  /** Open a file: a preview tab unless pinned, reusing the group's current preview. */
  open(path: string, o: { pin?: boolean; keepFocus?: boolean; side?: boolean; group?: number; line?: number; col?: number; endCol?: number; source?: boolean } = {}) {
    const gi = o.side ? this.groupForSide(this.activeGroup) : (o.group ?? this.activeGroup);
    const g = this.groups[gi] ?? this.group;
    const id = tabId(path);
    let tab = g.tabs.find((t) => t.id === id);
    if (tab) {
      if (o.pin) tab.preview = false;
    } else {
      tab = { id, path, preview: !o.pin };
      const preview = g.tabs.findIndex((t) => t.preview && !this.docs.get(t.path)?.dirty);
      if (preview >= 0) {
        const old = g.tabs[preview];
        g.tabs[preview] = tab;
        this.ensureDoc(path);
        this.releaseDoc(old.path);
      } else {
        const at = g.tabs.findIndex((t) => t.id === g.active);
        g.tabs.splice(at < 0 ? g.tabs.length : at + 1, 0, tab);
      }
    }
    g.active = id;
    this.activeGroup = this.groups.indexOf(g);
    const d = this.ensureDoc(path);
    if (o.source && d.md === 'reader') d.md = 'source';
    this.selected = path;
    this.addRecent(path);
    if (o.line) this.reveal = { tabId: id, group: g.id, line: o.line, col: o.col, endCol: o.endCol };
    if (!o.keepFocus) this.focusTick++;
    this.changed();
    this.syncWatch();
  }

  /** Compare a file with HEAD or with what is on disk now. */
  openDiff(path: string, source: DiffSource, group?: number) {
    const g = this.groups[group ?? this.activeGroup] ?? this.group;
    const id = tabId(path, source);
    if (!g.tabs.some((t) => t.id === id)) {
      const at = g.tabs.findIndex((t) => t.id === g.active);
      g.tabs.splice(at < 0 ? g.tabs.length : at + 1, 0, { id, path, diff: source, preview: false });
    }
    g.active = id;
    this.ensureDoc(path);
    this.changed();
  }

  /** Show a commit: a preview tab unless pinned, in the same place a preview file would take. */
  openCommit(hash: string, o: { label?: string; pin?: boolean } = {}) {
    const repo = this.gitTarget.repo;
    this.placeTab(`commit:${repo}:${hash}`, { path: '', commit: { hash, repo, label: o.label ?? hash.slice(0, 8) } }, !!o.pin);
  }

  openView(view: SpecialView, o: { pin?: boolean } = {}) {
    this.placeTab(viewId(view), { path: '', view }, !!o.pin);
  }

  /** Close a tab that shows no file, wherever it is. */
  closeSpecial(id: string) {
    this.groups.forEach((g, gi) => {
      const tab = g.tabs.find((t) => t.id === id);
      if (tab) this.removeTab(gi, tab);
    });
  }

  closeReviewOf(session: string) {
    for (const g of [...this.groups]) {
      for (const t of [...g.tabs]) {
        if (t.view?.kind === 'review' && t.view.session === session) this.removeTab(this.groups.indexOf(g), t);
      }
    }
  }

  private placeTab(id: string, fields: Omit<Tab, 'id' | 'preview'>, pin: boolean) {
    const g = this.group;
    const tab = g.tabs.find((t) => t.id === id);
    if (tab) {
      if (pin) tab.preview = false;
    } else {
      const next: Tab = { id, ...fields, preview: !pin };
      const preview = g.tabs.findIndex((t) => t.preview && !this.docs.get(t.path)?.dirty);
      if (preview >= 0) {
        const old = g.tabs[preview];
        g.tabs[preview] = next;
        this.releaseDoc(old.path);
      } else {
        const at = g.tabs.findIndex((t) => t.id === g.active);
        g.tabs.splice(at < 0 ? g.tabs.length : at + 1, 0, next);
      }
    }
    this.focusChanged();
    g.active = id;
    this.changed();
  }

  activate(gi: number, id: string) {
    const g = this.groups[gi];
    if (!g) return;
    if (this.activeGroup !== gi || g.active !== id) this.focusChanged();
    g.active = id;
    this.activeGroup = gi;
    const t = g.tabs.find((x) => x.id === id);
    if (t?.path) this.selected = t.path;
    if (t?.pane) this.paneFocus = { id: t.pane, tick: this.paneFocus.tick + 1 };
    this.focusTick++;
    this.changed();
  }

  focusGroup(gi: number) {
    if (this.activeGroup === gi) return;
    this.activeGroup = gi;
    const t = this.activeTab;
    if (t?.path) this.selected = t.path;
    this.changed();
  }

  pin(gi: number, id: string) {
    const t = this.groups[gi]?.tabs.find((x) => x.id === id);
    if (t) {
      t.preview = false;
      this.changed();
    }
  }

  private lastTabOf(path: string, except: Tab): boolean {
    return !this.groups.some((g) => g.tabs.some((t) => t !== except && t.path === path && !t.diff));
  }

  /** Close a tab, asking first when it holds the only view of unsaved edits. */
  closeTab(gi: number, id: string) {
    const tab = this.groups[gi]?.tabs.find((t) => t.id === id);
    if (!tab) return;
    if (tab.pane) {
      this.askClosePane(tab.pane);
      return;
    }
    if (!tab.diff && this.docs.get(tab.path)?.dirty && this.lastTabOf(tab.path, tab)) {
      this.closing = { group: gi, tab };
      this.changed();
      return;
    }
    this.removeTab(gi, tab);
  }

  // ---- agents in the editor (AgentPanes.tsx) -----------------------------------------

  /** Where a pane is shown now. */
  paneTabAt(id: string): { gi: number; tab: Tab } | null {
    for (let gi = 0; gi < this.groups.length; gi++) {
      const tab = this.groups[gi].tabs.find((t) => t.pane === id);
      if (tab) return { gi, tab };
    }
    return null;
  }

  /** Every console with a pane here. */
  get paneConsoles(): string[] {
    const tabs = this.groups.flatMap((g) => g.tabs.flatMap((t) => (t.pane ? [t.pane] : [])));
    return [...this.rightPanes, ...tabs].filter((id) => id !== ORCHESTRATOR_PANE);
  }

  private detachPane(id: string) {
    this.rightPanes = this.rightPanes.filter((p) => p !== id);
    if (this.rightActive === id) this.rightActive = this.rightPanes[0] ?? '';
    const at = this.paneTabAt(id);
    if (at) this.removeTab(at.gi, at.tab);
  }

  /** Show a pane in the side bar or as a tab of the active group; it has one place at a time. */
  placePane(id: string, where: 'side' | 'tab') {
    this.detachPane(id);
    if (where === 'side') {
      this.rightPanes = [...this.rightPanes, id];
      this.rightActive = id;
      this.rightOpen = true;
    } else {
      const g = this.group;
      const tab = paneTab(id);
      const at = g.tabs.findIndex((t) => t.id === g.active);
      g.tabs.splice(at < 0 ? g.tabs.length : at + 1, 0, tab);
      g.active = tab.id;
    }
    this.focusPane(id);
  }

  /** Bring a pane to the front wherever it is. False: it has no place here. */
  revealPane(id: string): boolean {
    const at = this.paneTabAt(id);
    if (at) {
      this.activate(at.gi, at.tab.id);
    } else if (this.rightPanes.includes(id)) {
      this.rightActive = id;
      this.rightOpen = true;
    } else {
      return false;
    }
    this.focusPane(id);
    return true;
  }

  private focusPane(id: string) {
    this.paneFocus = { id, tick: this.paneFocus.tick + 1 };
    this.changed();
  }

  /** The console is gone, or its view is simply closed. */
  forgetPane(id: string) {
    this.detachPane(id);
    if (this.closingPane === id) this.closingPane = '';
    this.changed();
  }

  /** The orchestrator just closes; a console asks first, as closing it stops the agent. */
  askClosePane(id: string) {
    if (id === ORCHESTRATOR_PANE) {
      this.forgetPane(id);
      return;
    }
    this.closingPane = id;
    this.changed();
  }

  /** Closes a diff tab wherever it is. */
  closeDiffTab(path: string, source: DiffSource) {
    const id = tabId(path, source);
    for (let gi = this.groups.length - 1; gi >= 0; gi--) {
      const tab = this.groups[gi].tabs.find((t) => t.id === id);
      if (tab) this.removeTab(gi, tab);
    }
  }

  private removeTab(gi: number, tab: Tab) {
    const g = this.groups[gi];
    if (!g) return;
    const i = g.tabs.indexOf(tab);
    if (i < 0) return;
    g.tabs.splice(i, 1);
    if (tab.diff?.startsWith('ide:')) this.ide.tabClosed(tab.diff.slice('ide:'.length));
    if (g.active === tab.id) g.active = g.tabs[Math.min(i, g.tabs.length - 1)]?.id ?? null;
    if (!g.tabs.length && this.groups.length > 1) {
      this.editors.delete(g.id);
      this.groups = this.groups.filter((x) => x !== g);
      this.activeGroup = Math.min(this.activeGroup, this.groups.length - 1);
    }
    this.releaseDoc(tab.path);
    this.changed();
  }

  /** The answer to "save before closing?". */
  resolveClosing(answer: 'save' | 'discard' | 'cancel') {
    const c = this.closing;
    this.closing = null;
    if (!c || answer === 'cancel') {
      this.changed();
      return;
    }
    if (answer === 'discard') {
      void drafts.remove(this.mid, c.tab.path);
      const d = this.docs.get(c.tab.path);
      if (d) d.dirty = false;
      this.removeTab(c.group, c.tab);
      return;
    }
    void this.save(c.tab.path).then((ok) => {
      if (ok) this.removeTab(this.groups.findIndex((g) => g.tabs.includes(c.tab)), c.tab);
    });
    this.changed();
  }

  closeOthers(gi: number, id: string) {
    const g = this.groups[gi];
    if (!g) return;
    for (const t of [...g.tabs]) if (t.id !== id && !t.pane && !(this.docs.get(t.path)?.dirty && !t.diff)) this.removeTab(this.groups.indexOf(g), t);
  }

  closeAll(gi: number) {
    const g = this.groups[gi];
    if (!g) return;
    for (const t of [...g.tabs]) if (!t.pane && (t.diff || !this.docs.get(t.path)?.dirty)) this.removeTab(this.groups.indexOf(g), t);
  }

  cycleTab(delta: number) {
    const g = this.group;
    if (g.tabs.length < 2) return;
    const i = g.tabs.findIndex((t) => t.id === g.active);
    this.activate(this.activeGroup, g.tabs[(i + delta + g.tabs.length) % g.tabs.length].id);
  }

  /** Show the active file in a new group beside this one. */
  splitRight() {
    const t = this.activeTab;
    if (!t) return;
    const from = this.activeGroup;
    const to = this.groupForSide(from);
    if (to === from) return;
    // a console has one view at a time: it moves over; a commit or a view has no file to open
    if (!t.path) this.moveTabToGroup(from, t.id, to);
    else this.open(t.path, { pin: true, group: to });
  }

  moveTabToGroup(fromGi: number, id: string, toGi: number) {
    const from = this.groups[fromGi];
    const to = this.groups[toGi];
    const tab = from?.tabs.find((t) => t.id === id);
    if (!tab || !to || from === to || to.tabs.some((t) => t.id === id)) return;
    from.tabs = from.tabs.filter((t) => t !== tab);
    if (from.active === id) from.active = from.tabs[0]?.id ?? null;
    to.tabs.push(tab);
    to.active = id;
    this.activeGroup = toGi;
    if (!from.tabs.length && this.groups.length > 1) {
      this.editors.delete(from.id);
      this.groups = this.groups.filter((g) => g !== from);
      this.activeGroup = this.groups.indexOf(to);
    }
    this.changed();
  }

  private addRecent(path: string) {
    this.recent = [path, ...this.recent.filter((p) => p !== path)].slice(0, MAX_RECENT);
  }

  // ---- file operations ---------------------------------------------------------------

  startInput(kind: TreeInput['kind'], dir: string, path?: string) {
    this.side = 'files';
    this.sideOpen = true;
    if (kind !== 'rename' && !this.expanded.has(dir)) this.toggleDir(dir, true);
    this.input = { kind, dir, path, value: kind === 'rename' && path ? nameOf(path) : '' };
    this.changed();
  }

  cancelInput() {
    this.input = null;
    this.changed();
  }

  /** Finish the row being typed; returns false when the name was refused. */
  async commitInput(value: string): Promise<boolean> {
    const inp = this.input;
    if (!inp) return true;
    const name = value.trim();
    if (!name || (inp.kind === 'rename' && name === nameOf(inp.path ?? ''))) {
      this.cancelInput();
      return true;
    }
    if (inp.kind === 'rename' ? /[/\\]/.test(name) : name.includes('\\') || name.split('/').some((s) => s === '..' || s === '.')) {
      toast.error('Use a plain name, without \\ or .. in it.');
      return false;
    }
    try {
      if (inp.kind === 'rename' && inp.path) {
        await this.moveTo(inp.path, `${dirOf(inp.path)}/${name}`);
      } else {
        // "src/new/file.ts" creates the folders on the way, as in VS Code
        const parts = name.split('/').filter(Boolean);
        const folders = inp.kind === 'folder' ? parts : parts.slice(0, -1);
        let dir = inp.dir;
        for (const f of folders) {
          dir = `${dir}/${f}`;
          const r = await this.run((c) => fsApi.mkdir(this.mid, dir, c));
          if (!r) return false;
        }
        if (inp.kind === 'file') {
          const st = await this.run((c) => fsApi.write(this.mid, { path: `${dir}/${parts[parts.length - 1]}`, content: '', unique: true, projectId: this.pid, ...c }));
          if (!st) return false;
          this.open(st.path, { pin: true });
        }
        void this.loadDir(inp.dir);
        if (dir !== inp.dir) this.revealInTree(inp.kind === 'file' ? `${dir}/${parts[parts.length - 1]}` : dir);
      }
      this.input = null;
      this.filesCache = null;
      this.gitSoon();
      this.changed();
      return true;
    } catch (err) {
      toast.error(errorMessage(err).includes('exist') ? 'Something with that name is already there.' : errorMessage(err));
      return false;
    }
  }

  /** Rename or move, keeping open tabs and unsaved edits on the new path. */
  async moveTo(from: string, to: string) {
    const st = await this.run((c) => fsApi.rename(this.mid, from, to, c));
    if (!st) return;
    await this.remap(from, st.path);
    this.debug.fileRenamed(from, st.path);
    void this.loadDir(dirOf(from));
    void this.loadDir(dirOf(st.path));
    if (this.expanded.delete(from)) {
      this.expanded.add(st.path);
      void this.loadDir(st.path);
    }
    if (this.selected === from) this.selected = st.path;
    this.filesCache = null;
    this.gitSoon();
    this.changed();
  }

  private async remap(from: string, to: string) {
    const moved = [...this.docs.values()].filter((d) => d.path === from || d.path.startsWith(`${from}/`));
    for (const old of moved) {
      const next = to + old.path.slice(from.length);
      const unsaved = old.dirty && old.model ? old.model.getValue() : null;
      for (const g of this.groups) {
        for (const t of g.tabs) {
          if (t.path !== old.path) continue;
          const nid = tabId(next, t.diff);
          if (g.active === t.id) g.active = nid;
          t.id = nid;
          t.path = next;
        }
      }
      if (old.model) {
        this.lsp.detach(old.model);
        old.model.dispose();
      }
      this.docs.delete(old.path);
      void drafts.remove(this.mid, old.path);
      const d = newDoc(next, old.md);
      this.docs.set(next, d);
      await this.load(d);
      if (unsaved !== null && d.model) d.model.setValue(unsaved);
    }
  }

  async trash(path: string) {
    const r = await this.run((c) => fsApi.trash(this.mid, path, this.pid, c));
    if (!r) return;
    for (const g of [...this.groups]) {
      for (const t of [...g.tabs]) {
        if (t.path === path || t.path.startsWith(`${path}/`)) {
          const d = this.docs.get(t.path);
          if (d) d.dirty = false;
          this.removeTab(this.groups.indexOf(g), t);
        }
      }
    }
    void this.loadDir(dirOf(path));
    this.filesCache = null;
    this.gitSoon();
    toast.success(`Moved to ${this.rel(r.trashedTo)}.`);
  }

  /** Show what was just added to a folder: selected, with the folder open. */
  private showAdded(path: string) {
    const dir = dirOf(path);
    void this.loadDir(dir);
    if (dir !== this.root && !this.expanded.has(dir)) this.toggleDir(dir, true);
    this.selected = path;
    this.revealTick++;
    this.filesCache = null;
    this.gitSoon();
    this.changed();
  }

  /** Paste what Copy or Cut in a tree put aside into this folder. Nothing gets replaced. */
  async pasteClip(dir: string) {
    const clip = fileClip.get();
    if (!clip) return;
    if (clip.machineId !== this.mid) {
      toast.error('Files can only be pasted on the machine they were copied on.');
      return;
    }
    const name = nameOf(clip.path);
    const into = (d: string) => `${d === '/' ? '' : d}/${name}`;
    const onItself = dir === clip.path || dir.startsWith(`${clip.path}/`);
    if (clip.cut) {
      if (onItself) {
        toast.error('A folder cannot be moved into itself.');
        return;
      }
      if (dirOf(clip.path) !== dir) await this.moveTo(clip.path, into(dir));
      fileClip.set(null);
      return;
    }
    // a folder pasted onto itself lands next to itself, as in VS Code
    const st = await this.run((c) => fsApi.copy(this.mid, clip.path, into(onItself ? dirOf(clip.path) : dir), true, c));
    if (st) this.showAdded(st.path);
  }

  /** Files from the computer, into this folder. What already has the name stays. */
  async pasteFiles(dir: string, files: File[]) {
    const sent = await sendPasted(files, async (f) => {
      const content = await fileToBase64(f);
      return this.run((c) => fsApi.write(this.mid, { path: `${dir === '/' ? '' : dir}/${f.name}`, content, encoding: 'base64', unique: true, projectId: this.pid, ...c }));
    });
    if (sent.length) this.showAdded(sent[sent.length - 1].path);
    return sent;
  }

  /** Files pasted while typing: they land next to the file, and the text gets the way to them. */
  async pasteIntoEditor(ed: monaco.editor.ICodeEditor, path: string, files: File[]) {
    const sent = await this.pasteFiles(dirOf(path), files);
    const at = ed.getSelection();
    if (!sent.length || !at) return;
    const markdown = /\.(md|mdx|markdown)$/i.test(path);
    const text = sent
      .map((st) => {
        const name = nameOf(st.path);
        if (!markdown) return name;
        return `${/^(png|jpe?g|gif|webp|svg)$/.test(extOf(name)) ? '!' : ''}[${name}](${encodeURI(name)})`;
      })
      .join('\n');
    ed.executeEdits('paste-file', [{ range: at, text, forceMoveMarkers: true }]);
    ed.focus();
  }

  /** Create a file with this text, making its folder when needed. */
  async writeNew(path: string, content: string, c: Confirm) {
    await fsApi.mkdir(this.mid, dirOf(path), c).catch(() => undefined);
    return fsApi.write(this.mid, { path, content, projectId: this.pid, ...c });
  }

  async duplicate(path: string) {
    const r = await api.fsRead(this.mid, path, MAX_VIEW + 1);
    if (r.binary) {
      toast.error('Binary files cannot be duplicated here.');
      return;
    }
    const st = await this.run((c) => fsApi.write(this.mid, { path, content: r.content, unique: true, projectId: this.pid, ...c }));
    if (!st) return;
    void this.loadDir(dirOf(path));
    this.filesCache = null;
    this.open(st.path, { pin: true });
  }
}
