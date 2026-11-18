// One editor card's view of a project (v0.9.0-beta.2, docs/API-studio.md §7): its tabs and
// groups, the file tree, search state, panes, terminals, the debugger and the link to Claude
// consoles, kept per place (viewKey): a single-part project's one card, a card placed in one part,
// or the Root card that shows every part as a top-level folder. It outlives the card's React tree
// so closing and reopening keeps tabs, undo and unsaved edits; the layout and the tab list also
// survive a reload (localStorage, drafts in IndexedDB).
//
// The files themselves are the tab's DocStore (docs.ts), shared by every card; a part's language
// servers, git, review and Laravel state are its PartService (partsvc.ts). Every path here is a
// FileKey (paths.ts): the same as the absolute path on This PC, "//m-<hex>/abs" elsewhere.

import { api, errorMessage } from '../../api';
import { sendPasted } from '../../board/files';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { limiter } from '../../lib/limiter';
import { packUpload } from '../../lib/pack';
import { mountOfPart, mountsOf, normalPlacement, partHoldingOn, projectParts, ROOT, rootFileOf, type Placement } from '../../lib/parts';
import type { CreateSession, Project, ProjectPart } from '../../lib/types';
import { filesApi, fsApi, type Confirm, type FileListing, type SearchFile, type SearchQuery } from '../api';
import { studioStore, waitJob } from '../store';
import type { TreeEntry } from '../types';
import type { OpenOptions, PartServiceApi, PartSpecialView, ViewApi, ViewProblems } from './contracts';
import { Debugger } from './debug';
import { canSplit, fromSaved, leaf, leaves, neighbour, removeLeaf, resize, splitLeaf, toSaved, type Edge, type GridNode } from './grid';
import { DEFAULT_EDITOR_PREFS, docStore, editorPrefs, MAX_EDIT, MAX_VIEW, type Doc, type EditorPrefs } from './docs';
import { fileClip } from './fileClip';
import { IdeLink } from './ideLink';
import { monaco } from './monaco';
import { PartService, type GitMark, type PartView } from './partsvc';
import { asFileKey, cleanAbs, fileKey, isRootKey, joinKey, keyOfUri, keyUnder, LOCAL_MACHINE, parentKey, PERSIST_VERSION, ROOT_KEY, splitKey, under, viewKey, ViewPaths, type FileKey, type Mount } from './paths';
import {
  DEFAULT_SEARCH,
  LAYOUT_VERSION,
  listUnder,
  MAX_RECENT,
  mergeBookmarks,
  mergeLegacy,
  readBookmarks,
  readLayout,
  readState,
  stateUnder,
  toolTabId,
  type PersistedTab,
  type SavedBookmark,
  type MdView,
  type PanelView,
  type Persisted,
  type ReadState,
  type SearchState,
  type SideView,
} from './persist';
import { toolPerArg, type ToolKind } from './toolKinds';
import type { RightTab } from './persist';
import { store } from '../../store';

export { DEFAULT_EDITOR_PREFS, MAX_EDIT, MAX_VIEW };
export type { Doc, Edge, EditorPrefs, GitMark, MdView, PanelView, SearchState, SideView };

/**
 * A card closed this long, with nothing unsaved only it holds and no debug session, is let go:
 * its tree, its hold on the files and parts, and its subscriptions. Opened again it is made anew
 * from what it saved (tabs, layout), as after a reload.
 */
const RELEASE_MS = 10 * 60_000;
/** The most paths one watch on a machine covers. */
const MAX_WATCH = 200;

export const MARKDOWN_EXT = new Set(['md', 'markdown', 'mdx']);

export type DiffSource = 'head' | 'index' | 'staged' | 'disk' | `commit:${string}` | `ide:${string}`;

export const extOf = (p: string) => {
  const base = p.slice(p.lastIndexOf('/') + 1);
  const i = base.lastIndexOf('.');
  return i > 0 ? base.slice(i + 1).toLowerCase() : '';
};
export const nameOf = (p: string) => p.slice(p.lastIndexOf('/') + 1);
/** The folder of a path or a key ("//m-ab/x" → "//m-ab/", the machine's "/"). */
export const dirOf = (p: string) => {
  const d = p.slice(0, p.lastIndexOf('/'));
  if (p.startsWith('//') && d.indexOf('/', 2) < 0) return `${d}/`;
  return d || '/';
};

export interface CommitTab {
  hash: string;
  /** The repository it belongs to, whichever one is selected later. */
  repo: string;
  label: string;
  /** The file last chosen in it. */
  file?: string;
  /** The part whose repository it is (id). */
  part?: string;
}

/** A view of its own in a tab, instead of a file. The part is the active one when not named. */
export type SpecialView =
  | { kind: 'review'; part?: string; session: string; path: string; agent: string }
  | { kind: 'history'; part?: string; path: string }
  | { kind: 'graph'; part?: string }
  | { kind: 'pr'; part?: string; number: number; title: string; repo: string };

const viewId = (v: PartSpecialView) =>
  v.kind === 'review' ? `review:${v.part}:${v.session}:${v.path}` : v.kind === 'history' ? `history:${v.path}` : v.kind === 'pr' ? `pr:${v.part}:${v.repo}:${v.number}` : `graph:${v.part}`;

export interface Tab {
  id: string;
  /** The file's key; "" when it shows no file: a pane, a tool, a commit or a special view. */
  path: FileKey | '';
  diff?: DiffSource;
  /**
   * A console instead of a file (AgentPanes.tsx): an agent of this editor, the orchestrator, a
   * terminal moved out of the panel, or a board's agent shown here (one console, two views).
   */
  pane?: string;
  /** A tool of the code view instead of a file (ToolTab.tsx): Chats, Memory, Vault, a browser… */
  tool?: { kind: ToolKind; arg?: string };
  /** Of a pane: a board's console shown here (one more view of it). Its close never stops it. */
  board?: boolean;
  /** A commit's details instead of a file (CommitView.tsx). */
  commit?: CommitTab & { part: string };
  view?: PartSpecialView;
  preview: boolean;
  /** Leads its group's strip, never a preview; Close Others, Close to the Right and Close All leave it. */
  pinned?: boolean;
  /** Nothing closes it until it is unlocked: no close key or button, no bulk close, no part removed or file trashed. */
  locked?: boolean;
}

/** The id of the orchestrator's pane; a console's pane has its session id. */
export const ORCHESTRATOR_PANE = 'orchestrator';

/** Why a split did nothing: the grid is full that way. */
const GRID_FULL = 'The editor holds four groups at most, two across and two down.';

/** A console shown here that lives on a board (a tile): closing its tab only closes this view of it. */
export function isBoardConsole(id: string): boolean {
  const s = store.getState().sessions?.[id];
  return !!s && !s.panel;
}

/** The hub's console as the browser knows it; undefined before the store has it (just after a reload). */
const sessionOf = (id: string) => store.getState().sessions?.[id];

/** A shell of this editor (not an agent): closing its tab kills it, as the panel's close does. */
export function isTerminalPane(id: string): boolean {
  const s = store.getState().sessions?.[id];
  return !!s && !!s.panel && s.agentKind !== 'agent';
}

export interface Group {
  id: string;
  tabs: Tab[];
  active: string | null;
}

/** A row of the tree: in a Root view the top rows are the parts, with their folder's key. */
export type ViewTreeEntry = TreeEntry & { key?: FileKey; part?: string };
export type TreeSlot = ViewTreeEntry[] | 'loading' | { error: string };

/** A row being typed into the tree: a new file or folder, or a rename. dir and path are keys. */
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

const tabId = (path: string, diff?: DiffSource) => (diff ? `diff:${diff}:${path}` : path);
const paneTab = (pane: string): Tab => ({ id: `pane:${pane}`, path: '', pane, preview: false });

/** A tab takes the pin and the lock of another (or of a saved one); both make it no preview. */
function withFlags(tab: Tab, from: { pinned?: boolean; locked?: boolean } | undefined): Tab {
  if (from?.pinned) tab.pinned = true;
  if (from?.locked) tab.locked = true;
  if (tab.pinned || tab.locked) tab.preview = false;
  return tab;
}

/** Where the strip's unpinned tabs start: the pinned ones lead it. */
const pinnedCount = (g: Group) => g.tabs.filter((t) => t.pinned).length;

/** A new tab goes after the active one but never among the pinned ones; a pinned one after the last pinned. */
function insertTab(g: Group, tab: Tab) {
  const pinned = pinnedCount(g);
  const at = g.tabs.findIndex((t) => t.id === g.active);
  g.tabs.splice(tab.pinned ? pinned : Math.max(at < 0 ? g.tabs.length : at + 1, pinned), 0, tab);
}

const LOCKED = 'Locked: unlock it to close.';

function readJSON<T>(key: string): T | null {
  try {
    const raw = prefs.get(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function forget(key: string) {
  try {
    window.localStorage.removeItem(`nb.${key}`);
  } catch {
    /* ignore */
  }
}

const OUTSIDE = 'Outside the project: read-only.';

const treeSlots = limiter(3);
/** Search and replace ask at most three parts at once. */
const searchSlots = limiter(3);

/** One part's answer to a search across the card's parts; files carry their keys. */
export interface PartSearch {
  part: PartService;
  files: (SearchFile & { key: FileKey })[];
  count: number;
  truncated: boolean;
  engine: string;
  /** This part could not answer (the others did). */
  error?: unknown;
}

/** A search over every part a card mounts: one section per part that has matches or failed, in part order. */
export interface ViewSearch {
  parts: PartSearch[];
  count: number;
  truncated: boolean;
}

const joinGlobs = (...g: (string | undefined)[]) => g.filter((x) => x && x.trim()).join(',');

const views = new Map<string, Workspace>();

/**
 * A view saves 400 ms after a change: a page put away or left (a reload, a closed tab, a phone
 * switching apps) writes what is still waiting at once, or a split or a tab just opened is lost.
 */
let flushInstalled = false;
function installFlush() {
  if (flushInstalled || typeof window === 'undefined' || typeof window.addEventListener !== 'function') return;
  flushInstalled = true;
  const flush = () => {
    for (const w of views.values()) w.flushSave();
  };
  window.addEventListener('pagehide', flush);
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'hidden') flush();
    });
  }
}

/**
 * The docs a card holds (its tabs) or its parts' language servers borrowed: the shared Doc
 * objects by key. get/has also take a plain absolute path, read as the card reads one (toKey).
 */
class ViewDocs extends Map<FileKey, Doc> {
  constructor(private coerce: (p: string) => FileKey | null) {
    super();
  }
  override get(k: string): Doc | undefined {
    const key = this.coerce(k);
    return key ? super.get(key) : undefined;
  }
  override has(k: string): boolean {
    const key = this.coerce(k);
    return !!key && super.has(key);
  }
  /** get by a key, as is. */
  at(k: FileKey): Doc | undefined {
    return super.get(k);
  }
  holds(k: FileKey): boolean {
    return super.has(k);
  }
}

interface Watch {
  id: string;
  off: () => void;
  key: string;
  renew: number;
}

export class Workspace implements ViewApi, PartView {
  readonly projectId: string;
  /** paths.ts viewKey: wb.<viewKey> in the browser, and the registry. */
  readonly viewKey: string;
  readonly placement: Placement;
  /** The card's paths: one part as itself, or the Root view's "/<part>/…". */
  paths: ViewPaths;
  /** The mounted parts, in part order. */
  parts: PartService[] = [];
  name: string;
  private project: Project;
  private partSubs = new Map<PartService, () => void>();
  /**
   * Parts of the project this card reads without mounting them (a file of another part, shown
   * as inside the project): held by `borrower` until the part changes or the card is let go.
   */
  private borrowed = new Map<string, PartService>();
  private readonly borrower = {};
  private released = false;

  version = 0;
  cursorVersion = 0;
  private listeners = new Set<() => void>();
  private cursorListeners = new Set<() => void>();

  readonly docs: ViewDocs;
  /** In the layout's reading order (grid.ts leaves): group 1 is the top left one. */
  groups: Group[] = [{ id: 'g1', tabs: [], active: null }];
  /** Where the groups lie: up to four, two by two (grid.ts). */
  grid: GridNode = leaf('g1');
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
  /** Keys of the files opened last. */
  recent: string[] = [];
  terminals: string[] = [];
  activeTerminal = '';
  private mdViews: Record<string, MdView> = {};

  /** Each folder's rows by its key (ROOT_KEY: the parts, in a Root view). */
  items = new Map<FileKey, TreeSlot>();
  /** Keys of the open folders. */
  expanded = new Set<FileKey>();
  truncated = new Set<FileKey>();
  /** The key of the selected row. */
  selected: FileKey | null = null;
  input: TreeInput | null = null;

  bookmarks: { path: string; line: number }[] = [];
  /** Moves when a pull request changed from here, so that the list follows. */
  pullsTick = 0;
  /** Task id → its last run. */
  taskRuns = new Map<string, { jobId: string; state: string; error: string }>();
  taskSelected = '';

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
  debug: Debugger;
  /** Ghost text requests in flight, for the status bar. */
  aiBusy = 0;
  /** Where questions from the editor go ("quick", "planner", "console:x", "group:x", "card:id"). */
  agentTarget = '';
  quickTurns: QuickTurn[] = [];
  private quickSeq = 1;
  /** The right side bar: panes by id, and which one shows. */
  rightOpen = false;
  rightW = 460;
  rightPanes: string[] = [];
  rightActive = '';
  /** The code view's right side bar (AgentPanes.tsx): the lead, the Agents list, or the pane shown (rightActive). */
  rightTab: RightTab = 'lead';
  /** This view has had a terminal: the panel starts one by itself only before that (BottomPanel). */
  hadTerminal = false;
  /** Which pane should take the keyboard. */
  paneFocus = { id: '', tick: 0 };
  /** The console asked to be closed: "stop the agent?". */
  closingPane = '';
  /** "Resume a chat…" is open, and where its console's pane goes. */
  resumingChats: 'side' | 'tab' | '' = '';
  /** The link to Claude consoles on the board that shows this editor. */
  readonly ide: IdeLink;
  actions: { ask?: (prompt?: string, ctx?: AgentContextInput) => void; aiEdit?: () => void; palette?: () => void; mention?: () => void; openWeb?: (url: string, machineId?: string) => void; pick?: (placeholder: string, items: { label: string; detail?: string; run: () => void }[]) => void; addToBoard?: (path: string) => void } = {};
  /** Root card: the part the Debug view runs in (id). */
  debugPart = '';
  private persistTimer = 0;
  /** A change not saved yet (persistTimer runs). */
  private persistDue = false;
  /** One watch per machine the card shows files of. */
  private watches = new Map<string, Watch>();
  private watchSeq = new Map<string, number>();
  private watchTimer = 0;
  private mounted = false;
  private mountCount = 0;
  private releaseTimer = 0;
  /** What the constructor subscribed to, undone when the card is let go. */
  private offs: (() => void)[] = [];

  /**
   * The view of a card placed in a project: at the root (every part, the default) or in one
   * part (a Placement, or the part itself). A single-part project has one view, keyed by the
   * project id as before. The same place on two boards is one view.
   */
  static get(project: Project, where?: Placement | ProjectPart): Workspace {
    const asked: Placement = !where ? ROOT : 'machineId' in where ? { partId: where.id } : where;
    const placement = normalPlacement(project, asked);
    const key = viewKey(project.id, projectParts(project).length, placement);
    let w = views.get(key);
    if (w) w.setProject(project);
    else {
      w = new Workspace(project, placement, key);
      views.set(key, w);
    }
    return w;
  }

  /** Every view of the tab (open cards and the ones not let go yet). */
  static all(): Iterable<Workspace> {
    return views.values();
  }

  private constructor(project: Project, placement: Placement, key: string) {
    this.projectId = project.id;
    this.viewKey = key;
    this.placement = placement;
    this.project = project;
    this.name = project.name;
    const { mounts, multi } = mountsOf(project, placement);
    this.paths = new ViewPaths(mounts, multi);
    this.docs = new ViewDocs((p) => this.toKey(p));
    this.ide = new IdeLink(this);
    this.setParts(project, mounts);
    this.debug = new Debugger(this);
    const markers = monaco.editor.onDidChangeMarkers(() => this.changed());
    this.offs.push(() => markers.dispose());
    this.offs.push(editorPrefs.subscribe(() => this.changed()));

    installFlush();
    const saved = this.loadState();
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
    this.hadTerminal = saved.hadTerminal === true || (saved.terminals?.length ?? 0) > 0;
    this.rightTab = saved.rightTab === 'agents' || (saved.rightTab === 'pane' && this.rightPanes.length) ? saved.rightTab : 'lead';
    this.showExcluded = saved.showExcluded ?? true;
    this.search = { ...DEFAULT_SEARCH, ...saved.search };
    this.recent = saved.recent ?? [];
    this.terminals = saved.terminals ?? [];
    this.activeTerminal = saved.activeTerminal ?? '';
    this.mdViews = saved.md ?? {};
    this.agentTarget = saved.agentTarget ?? '';
    this.debugPart = saved.debugPart ?? '';
    // readState made every saved path a key
    this.expanded = new Set(saved.expanded?.length ? (saved.expanded as FileKey[]) : this.defaultExpanded());
    for (const k of this.defaultExpanded()) this.expanded.add(k);
    this.bookmarks = this.loadBookmarks();
    // the groups with tabs, where they lay (a state of one row is put on the grid)
    const layout = readLayout(saved);
    if (layout) {
      const taken = new Set<string>();
      this.groups = layout.groups.map((g) => ({
        id: `g${this.groupSeq++}`,
        // pinned first, whatever order a state saved by hand has them in
        tabs: g.tabs
          .map((t) => withFlags(this.savedTab(t, taken), t))
          .sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned)),
        active: g.active,
      }));
      // an active id that names no tab of its group (a browser whose address another tab took
      // first got "#2"): the tab of that tool and address, else the first
      for (const g of this.groups) {
        if (g.tabs.some((t) => t.id === g.active)) continue;
        g.active = (g.tabs.find((t) => t.tool && toolTabId(t.tool.kind, t.tool.arg) === g.active) ?? g.tabs[0])?.id ?? null;
      }
      this.grid = fromSaved(layout.grid, this.groups.map((g) => g.id));
      this.activeGroup = Math.min(layout.activeGroup, this.groups.length - 1);
      for (const g of this.groups) for (const t of g.tabs) if (t.path) this.holdDoc(t.path);
    }
  }

  /** A saved tab as a tab again; a tool's id is made from its argument now (one per kind, a browser per address). */
  private savedTab(t: PersistedTab, taken: Set<string>): Tab {
    if (t.pane) return { ...paneTab(t.pane), ...(t.board ? { board: true } : {}) };
    if (t.tool) {
      const id = this.freeId(toolTabId(t.tool.kind, t.tool.arg), taken);
      return { id, path: '', tool: { ...t.tool }, preview: false };
    }
    return { id: tabId(t.path), path: t.path as FileKey, preview: t.preview };
  }

  /** An id no tab has yet: a tool opened twice by hand-edited state, or a browser that went to another's address. */
  private freeId(id: string, taken: Set<string> = new Set(this.groups.flatMap((g) => g.tabs.map((t) => t.id)))): string {
    let out = id;
    for (let n = 2; taken.has(out); n++) out = `${id}#${n}`;
    taken.add(out);
    return out;
  }

  // ---- parts and paths ---------------------------------------------------------------

  /** The folders always open: the tree root, and in a Root view every part's folder. */
  private defaultExpanded(): FileKey[] {
    return this.paths.multi ? [ROOT_KEY, ...this.paths.mounts.map((m) => m.rootKey)] : [this.paths.rootKey];
  }

  private setParts(project: Project, mounts: readonly Mount[]) {
    const all = projectParts(project);
    const next = mounts.map((m) => PartService.get(project, all.find((p) => p.id === m.partId)!));
    for (const old of this.parts) {
      if (next.includes(old)) continue;
      if (this.mounted) old.unmount(this);
      this.partSubs.get(old)?.();
      this.partSubs.delete(old);
      old.drop(this);
    }
    for (const p of next) {
      if (this.parts.includes(p)) continue;
      p.hold(this);
      this.partSubs.set(
        p,
        p.subscribe(() => this.changed()),
      );
      if (this.mounted) p.mount(this);
    }
    this.parts = next;
  }

  /** The project changed (a part renamed, added, removed or moved): the card follows, its tabs stay. */
  private setProject(project: Project) {
    const was = this.paths.mounts;
    const { mounts, multi } = mountsOf(project, this.placement);
    const renamed = this.name !== project.name || mounts.some((m, i) => m.name !== was[i]?.name || !!m.readOnly !== !!was[i]?.readOnly);
    this.project = project;
    this.name = project.name;
    const same = mounts.length === was.length && mounts.every((m, i) => m.partId === was[i].partId && m.machineId === was[i].machineId && m.root === was[i].root);
    this.paths = new ViewPaths(mounts, multi);
    // the same parts: their services take the new names (setParts does that for a new set)
    const all = projectParts(project);
    if (same) for (const m of mounts) PartService.get(project, all.find((p) => p.id === m.partId)!);
    else this.setParts(project, mounts);
    this.dropBorrowed(project);
    if (same) {
      // only a name or read-only changed: the card shows it now, not at its next change
      if (!renamed) return;
      if (this.paths.multi && this.items.has(ROOT_KEY)) this.items.set(ROOT_KEY, this.rootEntries());
      this.changed();
      return;
    }
    // the clean tabs of a part that went close; dirty and locked ones stay, as outside files
    for (const g of [...this.groups]) {
      for (const t of [...g.tabs]) {
        if (!t.path || t.locked || this.paths.mountOf(t.path) || this.docs.at(t.path)?.dirty) continue;
        if (this.partOf(t.path)) continue;
        this.removeTab(this.groups.indexOf(g), t);
      }
    }
    for (const k of this.defaultExpanded()) this.expanded.add(k);
    if (this.mounted) for (const d of this.expanded) void this.loadDir(d);
    this.changed();
  }

  get rootKey(): FileKey {
    return this.paths.rootKey;
  }

  label(key: FileKey): string {
    return this.paths.label(key);
  }

  relLabel(key: FileKey): string {
    return this.paths.relLabel(key);
  }

  partRel(key: FileKey): { mount: Mount; rel: string } | null {
    return this.paths.partRel(key);
  }

  resolve(input: string, machineId?: string): FileKey | null {
    return this.paths.resolve(input, machineId);
  }

  /**
   * A path a console on This PC names (the IDE link): through the project's root folder
   * ("<rootDir>/<part>/…", where root consoles work) it is that part's file, on the part's
   * machine; any other path is a real path on This PC.
   */
  cliKey(path: string): FileKey | null {
    const via = rootFileOf(this.project, path);
    return via ? fileKey(via.machineId, via.path) : this.resolve(path, LOCAL_MACHINE);
  }

  /** The mounted part holding a key; null outside every mounted part. */
  mountedPartOf(key: FileKey): PartService | null {
    if (isRootKey(key)) return null;
    const m = this.paths.mountOf(key);
    return m ? (this.parts.find((p) => p.partId === m.partId) ?? null) : null;
  }

  /**
   * The part of the project holding a key, mounted here or not; null outside every part. A part
   * the card does not mount is held (borrowed) until it changes or the card is let go.
   */
  partOf(key: FileKey): PartService | null {
    if (isRootKey(key)) return null;
    const here = this.mountedPartOf(key);
    if (here) return here;
    const { machineId, abs } = splitKey(key);
    const pt = partHoldingOn(this.project, machineId, abs);
    if (!pt) return null;
    // a card let go holds nothing new: what is there already, if anything
    if (this.released) return PartService.byRef(mountOfPart(this.project, pt).ref);
    const s = PartService.get(this.project, pt);
    const had = this.borrowed.get(s.ref);
    if (had !== s) {
      s.hold(this.borrower);
      this.borrowed.set(s.ref, s);
      had?.drop(this.borrower);
    }
    return s;
  }

  /** Lets go of borrowed parts: every one (no project), else the ones that left it, moved, or are mounted here now. */
  private dropBorrowed(project?: Project) {
    for (const [ref, s] of [...this.borrowed]) {
      const pt = project ? projectParts(project).find((p) => p.id === s.partId) : undefined;
      if (pt && !this.parts.includes(s)) {
        const m = mountOfPart(project!, pt);
        if (m.machineId === s.machineId && m.root === s.root) continue;
      }
      this.borrowed.delete(ref);
      s.drop(this.borrower);
    }
  }

  /** The part a file is saved through: the one holding it, else a mounted one on its machine. */
  private partFor(key: FileKey): PartService {
    if (isRootKey(key)) return this.activePart;
    const machine = splitKey(key).machineId;
    return this.partOf(key) ?? this.parts.find((p) => p.machineId === machine) ?? this.activePart;
  }

  /** Files came or went at these keys: the file lists of their parts, and the git of the ones a card shows, follow. */
  private filesTouched(...keys: FileKey[]) {
    for (const p of new Set(keys.map((k) => this.partOf(k)))) {
      if (!p) continue;
      p.filesChanged();
      if (p.mounted) p.gitSoon();
    }
  }

  /** The part of the active doc, else of the selected tree row, else the first mount. */
  get activePart(): PartService {
    const t = this.activeTab;
    const of = (k: string | null | undefined) => {
      const key = k ? asFileKey(k) : null;
      return key ? this.mountedPartOf(key) : null;
    };
    const tabPart = t?.commit?.part ?? t?.view?.part;
    return (tabPart ? this.parts.find((p) => p.partId === tabPart) : null) ?? of(t?.path) ?? of(this.selected) ?? this.parts[0];
  }

  /** Why a doc cannot be edited in this card ("" = it can): its own reason, or outside every part of the project. */
  readOnlyOf(doc: Doc): string {
    if (doc.readOnly) return doc.readOnly;
    return this.partOf(doc.path) ? '' : OUTSIDE;
  }

  /**
   * A path as this card reads it: a key stays a key; a plain absolute path is on the card's
   * machine (one part), or in the Root view on the machine of the part holding it (This PC
   * when none does). For callers that still hand plain paths; keys are what to pass.
   */
  toKey(p: string): FileKey | null {
    if (!p) return null;
    if (isRootKey(p)) return ROOT_KEY;
    if (p.startsWith('//')) return asFileKey(p);
    const abs = cleanAbs(p);
    if (abs === null) return null;
    if (!this.paths.multi) return fileKey(this.paths.mounts[0].machineId, abs);
    const local = fileKey(LOCAL_MACHINE, abs);
    if (this.paths.mountOf(local)) return local;
    let best: Mount | null = null;
    for (const m of this.paths.mounts) if (under(abs, m.root) && (!best || m.root.length > best.root.length)) best = m;
    return best ? fileKey(best.machineId, abs) : local;
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
    this.persistDue = true;
    this.persistTimer = window.setTimeout(() => this.persist(), 400);
  }

  setCursor(line: number, col: number, selected: number) {
    this.cursor = { line, col, selected };
    this.cursorVersion++;
    for (const fn of this.cursorListeners) fn();
  }

  // ---- what the store tells a holder (docs.ts Holder) -----------------------------------

  /** A doc this card holds got an edit: its own preview tabs of it stay. */
  edited(key: FileKey) {
    for (const g of this.groups) {
      for (const t of g.tabs) if (t.path === key && t.preview && !t.diff) t.preview = false;
    }
  }

  saveDoc(key: FileKey): Promise<boolean> {
    return this.save(key);
  }

  ready() {
    this.syncWatch();
  }

  renamed(from: FileKey, to: FileKey, doc?: Doc) {
    for (const g of this.groups) {
      for (const t of g.tabs) {
        if (t.path !== from) continue;
        const nid = tabId(to, t.diff);
        if (g.active === t.id) g.active = nid;
        t.id = nid;
        t.path = to;
      }
    }
    this.docs.delete(from);
    if (doc) this.docs.set(to, doc);
    if (this.mdViews[from]) {
      this.mdViews[to] = this.mdViews[from];
      delete this.mdViews[from];
    }
    this.recent = this.recent.map((p) => (p === from ? to : p));
    if (this.selected === from) this.selected = to;
    this.debug.fileRenamed(from, to);
    this.changed();
  }

  lent(key: FileKey, doc: Doc) {
    this.docs.set(key, doc);
  }

  gone(key: FileKey) {
    if (this.groups.some((g) => g.tabs.some((t) => t.path === key))) return;
    if (this.docs.delete(key)) this.changed();
  }

  /** Write what waits for the save timer now (the page is hidden or left). */
  flushSave() {
    if (!this.persistDue) return;
    window.clearTimeout(this.persistTimer);
    this.persist();
  }

  private persist() {
    this.persistDue = false;
    const p: Persisted = {
      v: PERSIST_VERSION,
      groups: this.groups.map((g) => {
        const flags = (t: Tab) => ({ ...(t.pinned ? { pinned: true } : {}), ...(t.locked ? { locked: true } : {}) });
        const active = g.tabs.find((t) => t.id === g.active);
        return {
          tabs: g.tabs
            .filter((t) => !t.diff && !t.commit && !t.view)
            .map((t): PersistedTab => (t.tool ? { path: '', preview: false, tool: { ...t.tool }, ...flags(t) } : { path: t.path, preview: t.preview, pane: t.pane, ...(t.board ? { board: true } : {}), ...flags(t) })),
          // a tool by its id as it is made again (its argument now)
          active: active?.tool ? toolTabId(active.tool.kind, active.tool.arg) : g.active?.startsWith('diff:') ? null : g.active,
        };
      }),
      activeGroup: this.activeGroup,
      grid: { v: LAYOUT_VERSION, root: toSaved(this.grid, this.groups.map((g) => g.id)) },
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
      rightTab: this.rightTab,
      ...(this.hadTerminal ? { hadTerminal: true } : {}),
      showExcluded: this.showExcluded,
      search: this.search,
      recent: this.recent,
      terminals: this.terminals,
      activeTerminal: this.activeTerminal,
      md: this.mdViews,
      agentTarget: this.agentTarget,
      ...(this.paths.multi && this.debugPart ? { debugPart: this.debugPart } : {}),
    };
    prefs.set(`wb.${this.viewKey}`, JSON.stringify(p));
  }

  /**
   * What the card saved; else what came before it: a card in one part takes over the
   * project-level state of the days when the editor showed the first part (wb.<project>) when
   * that state lies in this part, and the first Root open merges every old state of the project.
   */
  private loadState(): ReadState {
    const m0 = this.paths.mounts[0];
    const own = readJSON<Partial<Persisted>>(`wb.${this.viewKey}`);
    if (own) return readState(own, this.paths.multi ? LOCAL_MACHINE : m0.machineId);
    if (this.paths.multi) {
      const states: ReadState[] = [];
      const old = readJSON<Partial<Persisted>>(`wb.${this.projectId}`);
      if (old) states.push(this.legacyState(old));
      for (const m of this.paths.mounts) {
        const s = readJSON<Partial<Persisted>>(`wb.${this.projectId}~${m.partId}`);
        if (s) states.push(readState(s, m.machineId));
      }
      // the part the editor showed last, per browser: Root shows them all now
      forget(`wb.part.${this.projectId}`);
      return mergeLegacy(states, this.paths.mounts.map((m) => m.rootKey));
    }
    if (this.viewKey !== this.projectId) {
      const old = readJSON<Partial<Persisted>>(`wb.${this.projectId}`);
      if (old) {
        const s = readState(old, m0.machineId);
        if (stateUnder(s, m0.rootKey)) return s;
      }
    }
    return readState({}, m0.machineId);
  }

  /** An old project-level state read with the machine of the part it lies in. */
  private legacyState(old: Partial<Persisted>): ReadState {
    for (const m of this.paths.mounts) {
      const s = readState(old, m.machineId);
      if (stateUnder(s, m.rootKey)) return s;
    }
    // compat: wb.<project> was the first part's state (before v0.9.0-beta.2); nothing in it says otherwise
    return readState(old, this.paths.mounts[0].machineId);
  }

  /**
   * A per-card saved list or record (bookmarks, debug): its own, else (a part card) the
   * project-level one when it lies in this part, else (the first Root open) every old one merged.
   */
  saved<T>(prefix: string, read: (raw: unknown, machineId: string) => T, isUnder: (v: T, root: FileKey) => boolean, merge: (list: T[]) => T): T {
    const m0 = this.paths.mounts[0];
    const own = readJSON<unknown>(`${prefix}.${this.viewKey}`);
    if (own !== null) return read(own, this.paths.multi ? LOCAL_MACHINE : m0.machineId);
    const old = readJSON<unknown>(`${prefix}.${this.projectId}`);
    if (this.paths.multi) {
      const list: T[] = [];
      if (old !== null) {
        const m = this.paths.mounts.find((x) => isUnder(read(old, x.machineId), x.rootKey)) ?? m0;
        list.push(read(old, m.machineId));
      }
      for (const m of this.paths.mounts) {
        const s = readJSON<unknown>(`${prefix}.${this.projectId}~${m.partId}`);
        if (s !== null) list.push(read(s, m.machineId));
      }
      return merge(list);
    }
    if (this.viewKey !== this.projectId && old !== null) {
      const s = read(old, m0.machineId);
      if (isUnder(s, m0.rootKey)) return s;
    }
    return read(null, m0.machineId);
  }

  private loadBookmarks(): SavedBookmark[] {
    return this.saved('wb.bookmarks', readBookmarks, listUnder, mergeBookmarks);
  }

  log(line: string) {
    this.output = [...this.output.slice(-999), line];
    this.changed();
  }

  // ---- mount -------------------------------------------------------------------------

  /** The card opened: start what only runs while it is visible. */
  mount() {
    this.mountCount++;
    window.clearTimeout(this.releaseTimer);
    if (this.mountCount === 1) {
      this.mounted = true;
      for (const p of this.parts) p.mount(this);
    }
    for (const d of this.expanded) void this.loadDir(d);
    this.syncWatch();
  }

  /** The card closed: tabs stay, polling stops, servers stop after a while. */
  unmount() {
    this.mountCount = Math.max(0, this.mountCount - 1);
    if (this.mountCount > 0) return; // still open elsewhere
    this.mounted = false;
    this.persist();
    this.stopWatches();
    for (const p of this.parts) p.unmount(this);
    window.clearTimeout(this.releaseTimer);
    this.releaseTimer = window.setTimeout(() => this.release(), RELEASE_MS);
  }

  /** Lets a closed card go (RELEASE_MS); one with unsaved edits no other card holds, or a debug session, stays. */
  release(): boolean {
    if (this.mounted || this.debug.active) return false;
    for (const d of this.docs.values()) {
      if (d.dirty && ![...docStore.holders(d.path)].some((h) => h !== this)) return false;
    }
    if (views.get(this.viewKey) === this) views.delete(this.viewKey);
    window.clearTimeout(this.persistTimer);
    window.clearTimeout(this.watchTimer);
    this.persist();
    for (const k of [...this.docs.keys()]) {
      if (docStore.holders(k).has(this)) docStore.release(k, this);
      else docStore.unlend(k, this);
    }
    this.docs.clear();
    for (const off of this.offs.splice(0)) off();
    for (const [p, off] of this.partSubs) {
      off();
      p.drop(this);
    }
    this.partSubs.clear();
    this.released = true;
    this.dropBorrowed();
    return true;
  }

  /** Ask the hidden runner about the code; the answer streams into the Ask view. */
  /** `files` go along with the question, without showing in the conversation. */
  async askQuick(question: string, ctx: AgentContextInput | null, files: { path: string; text: string }[] = [], part: PartServiceApi = this.activePart) {
    const turn: QuickTurn = { id: this.quickSeq++, question, contextLabel: ctx?.label ?? '', jobId: '', status: 'running', text: '', error: '' };
    this.quickTurns = [...this.quickTurns.slice(-30), turn];
    this.changed();
    try {
      const attached = files.map((f) => `\n\nFile ${f.path}:\n\`\`\`\n${f.text}\n\`\`\``).join('');
      const j = await filesApi.aiAsk(part.ref, { question: question + attached, path: ctx?.rel ?? '', language: ctx?.language ?? '', selection: ctx?.code ?? '', startLine: ctx?.startLine ?? 0, endLine: ctx?.endLine ?? 0 });
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

  /** The editor settings: one set for every card. */
  get editorPrefs(): EditorPrefs {
    return editorPrefs.get();
  }

  setPrefs(patch: Partial<EditorPrefs>) {
    editorPrefs.set(patch);
  }

  /**
   * Where a terminal or agent of this card starts (POST /api/sessions fields): the part of a
   * folder (in that folder), else the card's part, else the project root (a Root card).
   */
  placeOf(dir?: FileKey): Pick<CreateSession, 'partId' | 'root' | 'machineId' | 'cwd'> {
    const k = dir ? this.toKey(dir) : null;
    if (k && !isRootKey(k)) {
      const { machineId, abs } = splitKey(k);
      const part = this.partOf(k);
      return part ? { partId: part.partId, machineId: part.machineId, cwd: abs } : { machineId, cwd: abs };
    }
    if (this.paths.multi) return { root: true };
    const p = this.parts[0];
    return { partId: p.partId, machineId: p.machineId, cwd: p.root };
  }

  /** Problems of the files this card mounts or holds, from every mounted part's language servers. */
  problems(): ViewProblems[] {
    const by = new Map<FileKey, ViewProblems>();
    for (const mk of monaco.editor.getModelMarkers({})) {
      const key = keyOfUri(mk.resource);
      if (!key || (!this.docs.holds(key) && !this.paths.mountOf(key))) continue;
      let p = by.get(key);
      if (!p) by.set(key, (p = { key, label: this.label(key), items: [] }));
      p.items.push({ line: mk.startLineNumber, col: mk.startColumn, message: mk.message, severity: mk.severity, source: mk.source });
    }
    for (const part of this.parts) {
      for (const d of part.lsp.closedDiagnostics()) if (!by.has(d.path)) by.set(d.path, { key: d.path, label: this.label(d.path), items: d.items });
    }
    return [...by.values()];
  }

  // ---- across the card's parts (search, Go to file, symbols) ---------------------------

  /**
   * Search every mounted part in parallel (three at a time). An include or exclude glob that
   * starts with a part's name ("admin/**") goes to that part only, without the name; with
   * include globs only for some parts, only those parts are searched (paths.ts routeGlobs). A
   * single-part card asks its part exactly as before. When every part fails, the first error is
   * thrown (a bad pattern says so once).
   */
  async searchParts(q: SearchQuery, signal?: AbortSignal): Promise<ViewSearch> {
    const inc = this.paths.routeGlobs(q.include ?? '');
    const exc = this.paths.routeGlobs(q.exclude ?? '');
    const targets = this.parts.filter((p) => !inc.size || inc.has('*') || inc.has(p.partId));
    const answers = await Promise.all(
      targets.map((part) =>
        searchSlots(() => filesApi.search(part.ref, { ...q, include: joinGlobs(inc.get(part.partId), inc.get('*')), exclude: joinGlobs(exc.get(part.partId), exc.get('*')) }, signal)).then(
          (r): PartSearch => ({ part, files: (r.files ?? []).map((f) => ({ ...f, key: part.keyOf(f.rel) })), count: r.count, truncated: r.truncated, engine: r.engine }),
          (error: unknown): PartSearch => ({ part, files: [], count: 0, truncated: false, engine: '', error }),
        ),
      ),
    );
    if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');
    const failed = answers.filter((a) => a.error !== undefined);
    if (answers.length && failed.length === answers.length) throw failed[0].error;
    return { parts: answers.filter((a) => a.files.length || a.error !== undefined), count: answers.reduce((n, a) => n + a.count, 0), truncated: answers.some((a) => a.truncated) };
  }

  /**
   * Replace in files of the card's parts (keys); files with unsaved edits are skipped and
   * returned. Each part gets its own call with its own paths; open docs that changed reload.
   */
  async replaceParts(q: SearchQuery & { replacement: string }, keys: FileKey[]): Promise<{ count: number; failed: { key: FileKey; error: string }[]; skipped: FileKey[] } | null> {
    const skipped = keys.filter((k) => docStore.get(k)?.dirty);
    const by = new Map<PartService, string[]>();
    for (const k of keys) {
      if (skipped.includes(k)) continue;
      const part = this.mountedPartOf(k);
      const rel = part?.relOf(k);
      if (!part || !rel) continue;
      by.set(part, [...(by.get(part) ?? []), rel]);
    }
    let count = 0;
    let ran = false;
    const failed: { key: FileKey; error: string }[] = [];
    for (const [part, paths] of by) {
      const r = await this.run((c) => searchSlots(() => filesApi.replace(part.ref, { ...q, paths, ...c })));
      if (!r) continue;
      ran = true;
      count += r.count;
      for (const f of r.files ?? []) {
        const key = part.keyOf(f.rel);
        if (f.error) failed.push({ key, error: f.error });
        else if (f.count && docStore.get(key)) void docStore.reload(key);
      }
      part.gitSoon();
    }
    return ran || !by.size ? { count, failed, skipped } : null;
  }

  /** Go to file: every mounted part's list, read in parallel. */
  fileLists(force = false): Promise<{ part: PartService; list: FileListing; error?: unknown }[]> {
    return Promise.all(
      this.parts.map((part) =>
        part.fileList(force).then(
          (list) => ({ part, list }),
          (error: unknown) => ({ part, list: { items: [], truncated: false, source: '' } as FileListing, error }),
        ),
      ),
    );
  }

  /** "#" in Go to file: every mounted part's language servers in parallel, merged, at most 200. */
  async workspaceSymbols(query: string): Promise<{ name: string; detail: string; path: FileKey; line: number; col: number; part: PartService }[]> {
    const lists = await Promise.all(this.parts.map((part) => part.lsp.workspaceSymbols(query).then((l) => l.map((x) => ({ ...x, part })), () => [])));
    return lists.flat().slice(0, 200);
  }

  // ---- tree --------------------------------------------------------------------------

  /** The Root view's top rows: one per part, in part order. */
  private rootEntries(): ViewTreeEntry[] {
    return this.paths.mounts.map((m) => ({ name: m.name, dir: true, size: 0, hidden: false, binary: false, key: m.rootKey, part: m.partId }));
  }

  async loadDir(dir: FileKey) {
    const k = this.toKey(dir);
    if (!k) return;
    if (isRootKey(k)) {
      this.items.set(ROOT_KEY, this.paths.multi ? this.rootEntries() : []);
      this.changed();
      return;
    }
    if (!this.items.has(k)) {
      this.items.set(k, 'loading');
      this.changed();
    }
    try {
      const { machineId, abs } = splitKey(k);
      const t = await treeSlots(() => fsApi.tree(machineId, abs, true));
      const entries = (t.entries ?? [])
        .filter((e) => e.name !== '.git' && e.name !== '.nextboard-trash')
        .sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
      this.items.set(k, entries);
      if (t.truncated) this.truncated.add(k);
      else this.truncated.delete(k);
      this.changed();
      void this.mountedPartOf(k)?.loadIgnored(
        k,
        entries.map((e) => e.name),
      );
    } catch (err) {
      this.items.set(k, { error: errorMessage(err) });
      this.changed();
    }
  }

  toggleDir(dir: FileKey, open?: boolean) {
    const k = this.toKey(dir);
    if (!k) return;
    const next = open ?? !this.expanded.has(k);
    if (next) {
      this.expanded.add(k);
      void this.loadDir(k);
    } else {
      this.expanded.delete(k);
    }
    this.changed();
    this.syncWatch();
  }

  collapseAll() {
    this.expanded = new Set([this.rootKey]);
    this.changed();
    this.syncWatch();
  }

  refreshTree() {
    for (const p of this.parts) p.filesChanged();
    for (const d of this.expanded) void this.loadDir(d);
    for (const p of this.parts) void p.refreshGit();
  }

  /** Open the folders above a file and scroll to it. */
  revealInTree(path: FileKey) {
    const k = this.toKey(path);
    if (!k) return;
    this.side = 'files';
    this.sideOpen = true;
    this.selected = k;
    const m = this.paths.mountOf(k);
    if (m) {
      if (this.paths.multi) this.expanded.add(ROOT_KEY);
      for (let d = parentKey(k); d && keyUnder(d, m.rootKey); d = parentKey(d)) {
        if (!this.expanded.has(d)) {
          this.expanded.add(d);
          void this.loadDir(d);
        }
        if (d === m.rootKey) break;
      }
    }
    this.revealTick++;
    this.changed();
    this.syncWatch();
  }

  // ---- local history (LocalHistoryView.tsx) -----------------------------------------------

  /** What a file holds for the owner now: the editor's text when it is open, else the disk's. */
  async currentText(path: FileKey): Promise<string> {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) return '';
    const model = docStore.get(k)?.model;
    if (model) return model.getValue();
    try {
      const { machineId, abs } = splitKey(k);
      const r = await api.fsRead(machineId, abs, MAX_VIEW);
      return r.content.startsWith('﻿') ? r.content.slice(1) : r.content;
    } catch {
      return ''; // it was deleted
    }
  }

  /** Bring back an earlier text: into the editor when the file is open, where Ctrl+Z undoes it. */
  async restoreText(path: FileKey, text: string) {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) return;
    const model = docStore.get(k)?.model;
    if (model) {
      // one step of its own, or Ctrl+Z also takes back what was typed before
      model.pushStackElement();
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text }], () => null);
      model.pushStackElement();
      this.open(k, { pin: true });
      toast.success('Restored in the editor. Save to keep it.');
      return;
    }
    const { machineId, abs } = splitKey(k);
    await fsApi.write(machineId, { path: abs, content: text, projectId: this.partFor(k).ref });
    this.open(k, { pin: true });
    toast.success('Restored.');
  }

  // ---- watching ----------------------------------------------------------------------

  /** Watch the open folders and files, one watch per machine; the hub stats them every few seconds. */
  syncWatch() {
    window.clearTimeout(this.watchTimer);
    this.watchTimer = window.setTimeout(() => void this.startWatches(), 500);
  }

  private async startWatches() {
    if (!this.mounted) return;
    const by = new Map<string, string[]>();
    const add = (raw: string) => {
      const k = asFileKey(raw);
      if (!k || isRootKey(k)) return;
      const { machineId, abs } = splitKey(k);
      const list = by.get(machineId) ?? [];
      if (!by.has(machineId)) by.set(machineId, list);
      list.push(abs);
    };
    for (const d of this.docs.values()) if (d.status === 'ready') add(d.path);
    for (const e of this.expanded) add(e);
    for (const machineId of [...this.watches.keys()]) if (!by.has(machineId)) this.stopWatch(machineId);
    await Promise.all([...by].map(([machineId, list]) => this.watchOn(machineId, [...new Set(list)].slice(0, MAX_WATCH))));
  }

  private async watchOn(machineId: string, paths: string[]) {
    const key = paths.slice().sort().join('\n');
    if (this.watches.get(machineId)?.key === key) return;
    this.stopWatch(machineId);
    if (!paths.length) return;
    const seq = (this.watchSeq.get(machineId) ?? 0) + 1;
    this.watchSeq.set(machineId, seq);
    try {
      const w = await fsApi.watch(machineId, paths, 60);
      // closed meanwhile, or a newer set of paths is already on its way
      if (!this.mounted || seq !== this.watchSeq.get(machineId)) {
        void fsApi.unwatch(w.watchId).catch(() => undefined);
        return;
      }
      const off = studioStore.watchFs(w.watchId, (ev) => {
        for (const c of ev.changes ?? []) {
          const abs = cleanAbs(c.path);
          if (abs) this.onDiskChange(fileKey(machineId, abs), c.exists, c.mtime);
        }
      });
      // a renewal buys 30 s: well before that, and a lost watch (hub restart) starts over
      const renew = window.setInterval(() => {
        void fsApi.renewWatch(w.watchId).catch(() => {
          if (this.watches.get(machineId)?.id !== w.watchId) return;
          this.stopWatch(machineId);
          this.syncWatch();
        });
      }, 15_000);
      this.watches.set(machineId, { id: w.watchId, off: () => off(), key, renew });
      // its first look is its starting point, never an event: an open file changed while no
      // watch looked (this card was just shown, the last card's watch already gone) shows here
      docStore.catchUp(machineId, w.stats ?? []);
    } catch {
      /* no watch: the tree refreshes by hand, a save still detects conflicts */
    }
  }

  private stopWatch(machineId: string) {
    const w = this.watches.get(machineId);
    if (!w) return;
    this.watches.delete(machineId);
    w.off();
    window.clearInterval(w.renew);
    void fsApi.unwatch(w.id).catch(() => undefined);
  }

  private stopWatches() {
    for (const machineId of [...this.watches.keys()]) this.stopWatch(machineId);
  }

  private onDiskChange(key: FileKey, exists: boolean, mtime: string | undefined) {
    if (this.expanded.has(key)) {
      this.partOf(key)?.filesChanged();
      void this.loadDir(key);
      this.mountedPartOf(key)?.gitSoon();
    }
    docStore.onDiskChange(key, exists, mtime);
  }

  // ---- documents ---------------------------------------------------------------------

  /** The part a card reaches a file through: the mounted part holding it, else the active one (a definition outside). */
  private viaFor(key: FileKey): PartService {
    return this.mountedPartOf(key) ?? this.activePart;
  }

  private holdDoc(key: FileKey): Doc {
    const d = docStore.acquire(key, this, this.viaFor(key), this.mdViews[key] ?? 'reader');
    this.docs.set(key, d);
    return d;
  }

  private releaseDoc(path: FileKey | '') {
    const k = path ? this.toKey(path) : null;
    if (!k) return;
    for (const g of this.groups) if (g.tabs.some((t) => t.path === k)) return;
    if (!this.docs.holds(k)) return;
    this.docs.delete(k);
    docStore.release(k, this);
    this.syncWatch();
  }

  private async formatDoc(d: Doc) {
    for (const ed of this.editors.values()) {
      if (ed.getModel() === d.model) {
        await ed.getAction('editor.action.formatDocument')?.run().catch(() => undefined);
        return;
      }
    }
  }

  async save(path: FileKey, opts: { force?: boolean; outside?: boolean } = {}): Promise<boolean> {
    const k = this.toKey(path);
    const d = k && !isRootKey(k) ? docStore.get(k) : undefined;
    if (!k || !d || this.readOnlyOf(d)) return false;
    return docStore.save(k, {
      ref: this.partFor(k).ref,
      run: this.run,
      force: opts.force,
      outside: opts.outside,
      format: this.editorPrefs.formatOnSave ? (doc) => this.formatDoc(doc as Doc) : undefined,
    });
  }

  async saveAll() {
    for (const d of [...this.docs.values()]) if (d.dirty && !this.readOnlyOf(d) && !d.conflict) await this.save(d.path);
  }

  /** Focus left the editor or the window (auto save "on focus change"). */
  focusChanged() {
    if (this.editorPrefs.autoSave !== 'onFocusChange') return;
    for (const d of this.docs.values()) if (d.dirty && !this.readOnlyOf(d) && !d.conflict) void this.save(d.path);
  }

  /** Write every pending draft now (the card is closing). */
  flushDrafts() {
    docStore.flushDrafts();
  }

  reload(path: FileKey): Promise<void> {
    const k = this.toKey(path);
    return k && !isRootKey(k) ? docStore.reload(k) : Promise.resolve();
  }

  reopenWithEncoding(path: FileKey, encoding: string): Promise<void> {
    const k = this.toKey(path);
    return k && !isRootKey(k) ? docStore.reopenWithEncoding(k, encoding) : Promise.resolve();
  }

  restoreDraft(path: FileKey) {
    const k = this.toKey(path);
    if (k && !isRootKey(k)) docStore.restoreDraft(k);
  }

  dropDraft(path: FileKey) {
    const k = this.toKey(path);
    if (k && !isRootKey(k)) docStore.dropDraft(k);
  }

  setMdView(path: FileKey, v: MdView) {
    const k = this.toKey(path);
    if (!k) return;
    const d = docStore.get(k);
    if (d) d.md = v;
    this.mdViews[k] = v;
    this.changed();
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
    return t?.path ? (this.docs.at(t.path) ?? null) : null;
  }

  activeEditor(): monaco.editor.IStandaloneCodeEditor | null {
    return this.editors.get(this.group.id) ?? null;
  }

  /** "Open to the side": the group to the right, else a new one there, else (the grid full) another group. */
  private groupForSide(from: number): number {
    const g = this.groups[from];
    if (!g) return from;
    const right = this.groups.findIndex((x) => x.id === neighbour(this.grid, g.id, 'right'));
    if (right >= 0) return right;
    const added = this.splitGroup(from, 'right');
    if (added) return this.groups.indexOf(added);
    const other = this.groups.findIndex((x) => x !== g);
    return other >= 0 ? other : from;
  }

  // ---- the split grid (grid.ts) ------------------------------------------------------

  /** Whether the group at gi can be split at that edge (the grid holds two across and two down). */
  canSplit(gi: number, edge: Edge): boolean {
    const g = this.groups[gi];
    return !!g && canSplit(this.grid, g.id, edge);
  }

  /** A new, empty group at an edge of the group at gi; null when the grid is full that way. The groups keep reading order. */
  private splitGroup(gi: number, edge: Edge): Group | null {
    const g = this.groups[gi];
    if (!g) return null;
    const added: Group = { id: `g${++this.groupSeq}`, tabs: [], active: null };
    const next = splitLeaf(this.grid, g.id, edge, added.id);
    if (!next) return null;
    const active = this.group;
    const all = [...this.groups, added];
    this.grid = next;
    this.groups = leaves(next).map((id) => all.find((x) => x.id === id)!);
    this.activeGroup = Math.max(0, this.groups.indexOf(active));
    return added;
  }

  /** A group leaves the grid (it has no tab left); the last one stays. The active one stays active, or the one in its place is. */
  private dropGroup(g: Group) {
    const at = this.groups.indexOf(g);
    if (at < 0 || this.groups.length <= 1) return;
    const active = this.group;
    this.editors.delete(g.id);
    this.groups = this.groups.filter((x) => x !== g);
    this.grid = removeLeaf(this.grid, g.id);
    this.activeGroup = active === g ? Math.min(at, this.groups.length - 1) : this.groups.indexOf(active);
  }

  /**
   * Split Right / Split Down of a tab, as in VS Code: a file opens again in a new group at that
   * edge; a console, a tool, a commit or a view (one place at a time) moves there, unless it is
   * the group's only tab. With the grid full that way, the group already there takes it.
   */
  splitTab(gi: number, id: string, edge: Edge = 'right'): boolean {
    const from = this.groups[gi];
    const t = from?.tabs.find((x) => x.id === id);
    if (!from || !t) return false;
    if (!t.path && from.tabs.length === 1) return false;
    const target = this.splitGroup(gi, edge) ?? this.groups.find((x) => x.id === neighbour(this.grid, from.id, edge));
    if (!target) {
      toast.info(GRID_FULL);
      return false;
    }
    if (t.path) this.open(t.path, { pin: true, group: this.groups.indexOf(target) });
    else this.moveTabToGroup(this.groups.indexOf(from), id, this.groups.indexOf(target));
    if (!target.tabs.length) this.dropGroup(target);
    this.changed();
    return true;
  }

  /** Split the active group's active tab at an edge (Ctrl+\ is right, Ctrl+K Ctrl+\ is down). */
  split(edge: Edge) {
    const g = this.group;
    if (g.active) this.splitTab(this.activeGroup, g.active, edge);
  }

  /** A tab dragged to a group's edge: it moves into a new group on that side (the whole group's place when it cannot split there). */
  moveTabToEdge(fromGi: number, id: string, toGi: number, edge: Edge) {
    const from = this.groups[fromGi];
    const to = this.groups[toGi];
    if (!from || !to || !from.tabs.some((t) => t.id === id)) return;
    // its only tab, to its own edge: nothing would be left behind
    if (from === to && from.tabs.length === 1) return;
    const added = this.splitGroup(toGi, edge);
    if (!added) {
      this.moveTabToGroup(fromGi, id, toGi);
      return;
    }
    this.moveTabToGroup(this.groups.indexOf(from), id, this.groups.indexOf(added));
    if (!added.tabs.length) this.dropGroup(added);
    this.changed();
  }

  /** A file dragged to a group's edge (from the tree): it opens in a new group on that side. */
  openAtEdge(path: string, toGi: number, edge: Edge) {
    const added = this.splitGroup(toGi, edge);
    this.open(path, { pin: true, group: added ? this.groups.indexOf(added) : toGi });
    if (added && !added.tabs.length) this.dropGroup(added);
    this.changed();
  }

  /** A sash dragged: the split at that path (grid.ts) gives its first side this share. */
  resizeSplit(path: string, at: number) {
    this.grid = resize(this.grid, path, at);
    this.changed();
  }

  /** Open a file: a preview tab unless pinned, reusing the group's current preview. */
  open(path: string, o: OpenOptions = {}) {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) return;
    const gi = o.side ? this.groupForSide(this.activeGroup) : (o.group ?? this.activeGroup);
    const g = this.groups[gi] ?? this.group;
    const id = tabId(k);
    let tab = g.tabs.find((t) => t.id === id);
    if (tab) {
      if (o.pin) tab.preview = false;
    } else {
      tab = { id, path: k, preview: !o.pin };
      const preview = g.tabs.findIndex((t) => t.preview && !(t.path && this.docs.at(t.path)?.dirty));
      if (preview >= 0) {
        const old = g.tabs[preview];
        g.tabs[preview] = tab;
        this.holdDoc(k);
        this.releaseDoc(old.path);
      } else {
        insertTab(g, tab);
      }
    }
    g.active = id;
    this.activeGroup = this.groups.indexOf(g);
    const d = this.holdDoc(k);
    if (o.source && d.md === 'reader') d.md = 'source';
    this.selected = k;
    this.addRecent(k);
    if (o.line) this.reveal = { tabId: id, group: g.id, line: o.line, col: o.col, endCol: o.endCol };
    if (!o.keepFocus) this.focusTick++;
    this.changed();
    this.syncWatch();
  }

  /** fileKey(machineId, abs), then open. */
  openReal(machineId: string, abs: string, o?: OpenOptions) {
    const p = cleanAbs(abs);
    if (p) this.open(fileKey(machineId, p), o);
  }

  /** Compare a file with HEAD or with what is on disk now. */
  openDiff(path: FileKey, source: DiffSource, group?: number) {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) return;
    const g = this.groups[group ?? this.activeGroup] ?? this.group;
    const id = tabId(k, source);
    if (!g.tabs.some((t) => t.id === id)) insertTab(g, { id, path: k, diff: source, preview: false });
    g.active = id;
    this.holdDoc(k);
    this.changed();
  }

  /** Show a commit of a part's repository: a preview tab unless pinned. */
  openCommit(part: PartServiceApi, hash: string, o: { label?: string; pin?: boolean } = {}) {
    const repo = part.gitTarget.repo;
    this.placeTab(`commit:${part.partId}:${repo}:${hash}`, { path: '', commit: { hash, repo, label: o.label ?? hash.slice(0, 8), part: part.partId } }, !!o.pin);
  }

  /** A view of its own in a tab; its part is the one named, else the file's (history), else the active one. */
  openView(view: SpecialView | PartSpecialView, o: { pin?: boolean } = {}) {
    let part = view.part;
    let v = view;
    if (view.kind === 'history') {
      const k = this.toKey(view.path);
      if (!k) return;
      part ||= this.partOf(k)?.partId;
      v = { ...view, path: k };
    }
    const full = { ...v, part: part || this.activePart.partId } as PartSpecialView;
    this.placeTab(viewId(full), { path: '', view: full }, !!o.pin);
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
      const preview = g.tabs.findIndex((t) => t.preview && !(t.path && this.docs.at(t.path)?.dirty));
      if (preview >= 0) {
        const old = g.tabs[preview];
        g.tabs[preview] = next;
        this.releaseDoc(old.path);
      } else {
        insertTab(g, next);
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

  /**
   * Pin or unpin a tab (no `on`: the other way round), as in VS Code: pinned, it moves after the
   * tabs pinned before it and stops being a preview; unpinned, it leads the unpinned ones.
   */
  setPinned(gi: number, id: string, on?: boolean) {
    const g = this.groups[gi];
    const t = g?.tabs.find((x) => x.id === id);
    if (!g || !t) return;
    const next = on ?? !t.pinned;
    if (!!t.pinned === next) return;
    g.tabs.splice(g.tabs.indexOf(t), 1);
    if (next) {
      t.pinned = true;
      t.preview = false;
    } else {
      delete t.pinned;
    }
    g.tabs.splice(pinnedCount(g), 0, t);
    this.changed();
  }

  /** Lock or unlock a tab (no `on`: the other way round). Locked, nothing closes it; it stays where it is. */
  setLocked(gi: number, id: string, on?: boolean) {
    const t = this.groups[gi]?.tabs.find((x) => x.id === id);
    if (!t) return;
    const next = on ?? !t.locked;
    if (!!t.locked === next) return;
    if (next) {
      t.locked = true;
      t.preview = false;
    } else {
      delete t.locked;
    }
    this.changed();
  }

  private lastTabOf(path: FileKey, except: Tab): boolean {
    return !this.groups.some((g) => g.tabs.some((t) => t !== except && t.path === path && !t.diff));
  }

  /** Another card shows the file too. */
  private shownElsewhere(key: FileKey): boolean {
    return [...docStore.holders(key)].some((h) => h !== this);
  }

  /** Close a tab, asking first when it holds the only view of unsaved edits (in any card). A locked one says why it stays. */
  closeTab(gi: number, id: string) {
    const tab = this.groups[gi]?.tabs.find((t) => t.id === id);
    if (!tab) return;
    if (tab.locked) {
      toast.info(LOCKED);
      return;
    }
    if (tab.pane) {
      this.askClosePane(tab.pane);
      return;
    }
    if (tab.path && !tab.diff && this.docs.at(tab.path)?.dirty && this.lastTabOf(tab.path, tab) && !this.shownElsewhere(tab.path)) {
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
    if (!this.rightPanes.length && this.rightTab === 'pane') this.rightTab = 'lead';
    const at = this.paneTabAt(id);
    if (at) this.removeTab(at.gi, at.tab);
  }

  /** Show a pane in the side bar or as a tab of the active group; it has one place at a time. A tab keeps its pin and lock. */
  placePane(id: string, where: 'side' | 'tab') {
    const was = this.paneTabAt(id)?.tab;
    // the side bar has no lock: a locked tab stays in its strip
    if (where === 'side' && was?.locked) {
      toast.info('Locked: unlock it to move it to the side bar.');
      return;
    }
    this.detachPane(id);
    if (where === 'side') {
      this.rightPanes = [...this.rightPanes, id];
      this.rightActive = id;
      this.rightTab = 'pane';
      this.rightOpen = true;
    } else {
      const g = this.group;
      const tab = withFlags(paneTab(id), was);
      insertTab(g, tab);
      g.active = tab.id;
    }
    this.focusPane(id);
  }

  /**
   * A console as a tab (contract: the code view's Agents list, terminals in the editor area): its
   * tab if it has one, focused; else a new tab in the group asked for (the active one). It leaves
   * the side bar and the panel: a console has one place in an editor. A board's console stays on
   * its board; the tab is one more view of it, and opening it wakes it when it sleeps (its
   * terminal connects).
   */
  openPane(id: string, o: { group?: number } = {}) {
    const at = this.paneTabAt(id);
    if (at) {
      this.activate(at.gi, at.tab.id);
      this.focusPane(id);
      return;
    }
    this.rightPanes = this.rightPanes.filter((p) => p !== id);
    if (this.rightActive === id) this.rightActive = this.rightPanes[0] ?? '';
    if (!this.rightPanes.length && this.rightTab === 'pane') this.rightTab = 'lead';
    this.dropTerminal(id);
    const g = this.groups[o.group ?? this.activeGroup] ?? this.group;
    const tab = paneTab(id);
    if (isBoardConsole(id)) tab.board = true;
    insertTab(g, tab);
    g.active = tab.id;
    this.activeGroup = this.groups.indexOf(g);
    this.focusChanged();
    this.focusPane(id);
  }

  /** A console shown here that lives on a board: marked on its tab, else the store says so. */
  isBoardPane(id: string): boolean {
    return !!this.paneTabAt(id)?.tab.board || isBoardConsole(id);
  }

  /** A terminal of the panel moves to the editor area (a tab of the active group); its shell keeps running. */
  movePanelTerminalToEditor(id: string, o: { group?: number } = {}) {
    this.openPane(id, o);
  }

  /** A terminal's tab goes back to the panel, as its active terminal; its shell keeps running. A locked tab stays. */
  movePaneToPanel(id: string) {
    const at = this.paneTabAt(id);
    if (at?.tab.locked) {
      toast.info('Locked: unlock it to move it.');
      return;
    }
    this.detachPane(id);
    this.hadTerminal = true;
    if (!this.terminals.includes(id)) this.terminals = [...this.terminals, id];
    this.activeTerminal = id;
    this.panelOpen = true;
    this.panelView = 'terminal';
    this.changed();
  }

  /** Out of the panel's list (it moved to a tab), its next terminal shown instead. */
  private dropTerminal(id: string) {
    if (!this.terminals.includes(id)) return;
    this.hadTerminal = true;
    this.terminals = this.terminals.filter((t) => t !== id);
    if (this.activeTerminal === id) this.activeTerminal = this.terminals[0] ?? '';
  }

  // ---- tools as tabs (the code view, ToolTab.tsx) -------------------------------------

  /** Where a tool's tab is: one of that kind (a browser: with that address). */
  toolTabAt(kind: ToolKind, arg?: string): { gi: number; tab: Tab } | null {
    for (let gi = 0; gi < this.groups.length; gi++) {
      const tab = this.groups[gi].tabs.find((t) => t.tool?.kind === kind && (!toolPerArg(kind) || (t.tool.arg ?? '') === (arg ?? '')));
      if (tab) return { gi, tab };
    }
    return null;
  }

  /** Open a tool as a tab: the one open already is focused, else a new tab joins the active group. Never a preview. */
  openTool(kind: ToolKind, arg?: string) {
    const at = this.toolTabAt(kind, arg);
    if (at) {
      this.activate(at.gi, at.tab.id);
      return;
    }
    const g = this.group;
    const tab: Tab = { id: this.freeId(toolTabId(kind, arg)), path: '', tool: { kind, ...(arg ? { arg } : {}) }, preview: false };
    insertTab(g, tab);
    this.focusChanged();
    g.active = tab.id;
    this.changed();
  }

  /** A tool's tab took another argument (a browser went to another address): kept with the tab, its id stays. */
  setToolArg(id: string, arg: string) {
    for (const g of this.groups) {
      const t = g.tabs.find((x) => x.id === id);
      if (!t?.tool || (t.tool.arg ?? '') === arg) continue;
      t.tool = { kind: t.tool.kind, ...(arg ? { arg } : {}) };
      this.changed();
    }
  }

  /** Bring a pane to the front wherever it is. False: it has no place here. */
  revealPane(id: string): boolean {
    const at = this.paneTabAt(id);
    if (at) {
      this.activate(at.gi, at.tab.id);
    } else if (this.rightPanes.includes(id)) {
      this.rightActive = id;
      this.rightTab = 'pane';
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

  /**
   * The orchestrator and a board's console just close (the console stays on its board); a console
   * of this editor asks first, as closing it stops the agent or kills the terminal. A locked tab
   * of any stays.
   */
  askClosePane(id: string) {
    if (this.paneTabAt(id)?.tab.locked) {
      toast.info(LOCKED);
      return;
    }
    // a board's console only leaves this editor; one the browser does not know yet is never offered to stop
    if (id === ORCHESTRATOR_PANE || this.isBoardPane(id) || !sessionOf(id)) {
      this.forgetPane(id);
      return;
    }
    this.closingPane = id;
    this.changed();
  }

  /** Closes a diff tab wherever it is. */
  closeDiffTab(path: FileKey, source: DiffSource) {
    const k = this.toKey(path);
    if (!k) return;
    const id = tabId(k, source);
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
    if (!g.tabs.length) this.dropGroup(g);
    this.releaseDoc(tab.path);
    this.changed();
  }

  /** The answer to "save before closing?". */
  resolveClosing(answer: 'save' | 'discard' | 'cancel') {
    const c = this.closing;
    this.closing = null;
    if (!c || answer === 'cancel' || !c.tab.path) {
      this.changed();
      return;
    }
    if (answer === 'discard') {
      docStore.discard(c.tab.path);
      this.removeTab(c.group, c.tab);
      return;
    }
    void this.save(c.tab.path).then((ok) => {
      if (ok) this.removeTab(this.groups.findIndex((g) => g.tabs.includes(c.tab)), c.tab);
    });
    this.changed();
  }

  /** What Close Others, Close to the Right and Close All take: no pane, no pinned or locked tab, no unsaved file. */
  private bulkCloses(t: Tab): boolean {
    return !t.pane && !t.pinned && !t.locked && (!!t.diff || !(t.path && this.docs.at(t.path)?.dirty));
  }

  closeOthers(gi: number, id: string) {
    const g = this.groups[gi];
    if (!g) return;
    for (const t of [...g.tabs]) if (t.id !== id && this.bulkCloses(t)) this.removeTab(this.groups.indexOf(g), t);
  }

  closeRight(gi: number, id: string) {
    const g = this.groups[gi];
    const i = g ? g.tabs.findIndex((t) => t.id === id) : -1;
    if (!g || i < 0) return;
    for (const t of g.tabs.slice(i + 1)) if (this.bulkCloses(t)) this.removeTab(this.groups.indexOf(g), t);
  }

  closeAll(gi: number) {
    const g = this.groups[gi];
    if (!g) return;
    for (const t of [...g.tabs]) if (this.bulkCloses(t)) this.removeTab(this.groups.indexOf(g), t);
  }

  cycleTab(delta: number) {
    const g = this.group;
    if (g.tabs.length < 2) return;
    const i = g.tabs.findIndex((t) => t.id === g.active);
    this.activate(this.activeGroup, g.tabs[(i + delta + g.tabs.length) % g.tabs.length].id);
  }

  /** Show the active tab in a new group to the right (Ctrl+\). */
  splitRight() {
    this.split('right');
  }

  /** Show the active tab in a new group below (Ctrl+K Ctrl+\). */
  splitDown() {
    this.split('bottom');
  }

  moveTabToGroup(fromGi: number, id: string, toGi: number) {
    const from = this.groups[fromGi];
    const to = this.groups[toGi];
    const tab = from?.tabs.find((t) => t.id === id);
    if (!tab || !to || from === to || to.tabs.some((t) => t.id === id)) return;
    from.tabs = from.tabs.filter((t) => t !== tab);
    if (from.active === id) from.active = from.tabs[0]?.id ?? null;
    // its pin and lock go along: a pinned tab joins the other strip's pinned ones
    to.tabs.splice(tab.pinned ? pinnedCount(to) : to.tabs.length, 0, tab);
    to.active = id;
    this.activeGroup = toGi;
    if (!from.tabs.length) this.dropGroup(from);
    this.activeGroup = this.groups.indexOf(to);
    this.changed();
  }

  private addRecent(key: FileKey) {
    this.recent = [key, ...this.recent.filter((p) => p !== key)].slice(0, MAX_RECENT);
  }

  // ---- file operations ---------------------------------------------------------------

  /** A part's own folder: renaming, moving or trashing it would leave the part pointing nowhere. */
  private isPartFolder(k: FileKey): boolean {
    return isRootKey(k) || this.paths.mounts.some((m) => m.rootKey === k);
  }

  private refusePartFolder(k: FileKey): boolean {
    if (!this.isPartFolder(k)) return false;
    toast.info(isRootKey(k) ? 'Pick a part’s folder first: the root of the project is a view of its parts.' : 'This is a part’s own folder. Change it in the project’s parts instead.');
    return true;
  }

  startInput(kind: TreeInput['kind'], dir: FileKey, path?: string) {
    const d = this.toKey(dir);
    if (!d) return;
    if (isRootKey(d) && kind !== 'rename') {
      toast.info('Pick a part’s folder first: the root of the project is a view of its parts.');
      return;
    }
    const target = path ? this.toKey(path) : null;
    if (kind === 'rename' && target && this.refusePartFolder(target)) return;
    this.side = 'files';
    this.sideOpen = true;
    if (kind !== 'rename' && !this.expanded.has(d)) this.toggleDir(d, true);
    const p = path ? (this.toKey(path) ?? undefined) : undefined;
    this.input = { kind, dir: d, path: p, value: kind === 'rename' && p ? nameOf(p) : '' };
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
    const dirKey = this.toKey(inp.dir);
    if (!dirKey || isRootKey(dirKey)) return false;
    try {
      if (inp.kind === 'rename' && inp.path) {
        const from = this.toKey(inp.path);
        const parent = from ? parentKey(from) : null;
        if (from && parent) await this.moveTo(from, joinKey(parent, name));
      } else {
        const { machineId, abs: dirAbs } = splitKey(dirKey);
        const ref = this.partFor(dirKey).ref;
        // "src/new/file.ts" creates the folders on the way, as in VS Code
        const parts = name.split('/').filter(Boolean);
        const folders = inp.kind === 'folder' ? parts : parts.slice(0, -1);
        let dir = dirAbs === '/' ? '' : dirAbs;
        for (const f of folders) {
          dir = `${dir}/${f}`;
          const at = dir;
          const r = await this.run((c) => fsApi.mkdir(machineId, at, c));
          if (!r) return false;
        }
        if (inp.kind === 'file') {
          const file = `${dir}/${parts[parts.length - 1]}`;
          const st = await this.run((c) => fsApi.write(machineId, { path: file, content: '', unique: true, projectId: ref, ...c }));
          if (!st) return false;
          this.open(fileKey(machineId, st.path), { pin: true });
        }
        void this.loadDir(dirKey);
        if ((dir || '/') !== dirAbs) this.revealInTree(fileKey(machineId, inp.kind === 'file' ? `${dir}/${parts[parts.length - 1]}` : dir));
      }
      this.input = null;
      this.filesTouched(dirKey);
      this.changed();
      return true;
    } catch (err) {
      toast.error(errorMessage(err).includes('exist') ? 'Something with that name is already there.' : errorMessage(err));
      return false;
    }
  }

  /** Rename or move on one machine, keeping open tabs (in every card) and unsaved edits on the new path. */
  async moveTo(from: FileKey, to: FileKey) {
    const a = this.toKey(from);
    const b = this.toKey(to);
    if (!a || !b || isRootKey(a) || isRootKey(b) || this.refusePartFolder(a)) return;
    const src = splitKey(a);
    const dst = splitKey(b);
    if (src.machineId !== dst.machineId) {
      toast.error('Files can only be moved on the machine they are on.');
      return;
    }
    const st = await this.run((c) => fsApi.rename(src.machineId, src.abs, dst.abs, c));
    if (!st) return;
    const moved = fileKey(src.machineId, st.path);
    await docStore.remap(a, moved);
    this.debug.fileRenamed(a, moved);
    const pa = parentKey(a);
    const pm = parentKey(moved);
    if (pa) void this.loadDir(pa);
    if (pm) void this.loadDir(pm);
    if (this.expanded.delete(a)) {
      this.expanded.add(moved);
      void this.loadDir(moved);
    }
    if (this.selected === a) this.selected = moved;
    this.filesTouched(a, moved);
    this.changed();
  }

  async trash(path: FileKey) {
    const k = this.toKey(path);
    if (!k || this.refusePartFolder(k)) return;
    const { machineId, abs } = splitKey(k);
    const r = await this.run((c) => fsApi.trash(machineId, abs, this.partFor(k).ref, c));
    if (!r) return;
    // a locked tab stays, with its edits, showing its file as deleted on disk (Save it again)
    const kept = new Set(this.groups.flatMap((g) => g.tabs.flatMap((t) => (t.locked && t.path && keyUnder(t.path, k) ? [t.path] : []))));
    for (const g of [...this.groups]) {
      for (const t of [...g.tabs]) {
        if (t.path && keyUnder(t.path, k) && !t.locked) {
          const d = this.docs.at(t.path);
          if (d && !kept.has(t.path)) d.dirty = false;
          this.removeTab(this.groups.indexOf(g), t);
        }
      }
    }
    for (const key of kept) docStore.onDiskChange(key, false, undefined);
    const parent = parentKey(k);
    if (parent) void this.loadDir(parent);
    this.filesTouched(k);
    const to = cleanAbs(r.trashedTo);
    toast.success(`Moved to ${to ? this.relLabel(fileKey(machineId, to)) : r.trashedTo}.`);
  }

  /** Show what was just added to a folder: selected, with the folder open. */
  private showAdded(key: FileKey) {
    const dir = parentKey(key);
    if (dir) {
      void this.loadDir(dir);
      if (dir !== this.rootKey && !this.expanded.has(dir)) this.toggleDir(dir, true);
    }
    this.selected = key;
    this.revealTick++;
    this.filesTouched(key);
    this.changed();
  }

  /** Paste what Copy or Cut in a tree put aside into this folder. Nothing gets replaced. */
  async pasteClip(dir: FileKey) {
    const clip = fileClip.get();
    const d = this.toKey(dir);
    if (!clip || !d || (isRootKey(d) && this.refusePartFolder(d))) return;
    const into = splitKey(d);
    const src = clip.path.startsWith('//') ? asFileKey(clip.path) : cleanAbs(clip.path) ? fileKey(clip.machineId, clip.path) : null;
    if (!src || splitKey(src).machineId !== into.machineId) {
      toast.error('Files can only be pasted on the machine they were copied on.');
      return;
    }
    const srcAbs = splitKey(src).abs;
    const name = nameOf(srcAbs);
    const target = (folder: string) => `${folder === '/' ? '' : folder}/${name}`;
    const onItself = keyUnder(d, src);
    if (clip.cut) {
      if (onItself) {
        toast.error('A folder cannot be moved into itself.');
        return;
      }
      if (parentKey(src) !== d) await this.moveTo(src, fileKey(into.machineId, target(into.abs)));
      fileClip.set(null);
      return;
    }
    // a folder pasted onto itself lands next to itself, as in VS Code
    const dest = target(onItself ? dirOf(srcAbs) : into.abs);
    const st = await this.run((c) => fsApi.copy(into.machineId, srcAbs, dest, true, c));
    if (st) this.showAdded(fileKey(into.machineId, st.path));
  }

  /** Files from the computer, into this folder. What already has the name stays. */
  async pasteFiles(dir: FileKey, files: File[]) {
    const d = this.toKey(dir);
    if (!d || (isRootKey(d) && this.refusePartFolder(d))) return [];
    const { machineId, abs } = splitKey(d);
    const ref = this.partFor(d).ref;
    const sent = await sendPasted(files, async (f) => {
      const packed = await packUpload(f);
      return this.run((c) => api.fsUpload(machineId, packed, { path: `${abs === '/' ? '' : abs}/${f.name}`, unique: true, projectId: ref, ...c }));
    });
    if (sent.length) this.showAdded(fileKey(machineId, sent[sent.length - 1].path));
    return sent;
  }

  /** Files pasted while typing: they land next to the file, and the text gets the way to them. */
  async pasteIntoEditor(ed: monaco.editor.ICodeEditor, path: FileKey, files: File[]) {
    const sent = await this.pasteFiles(parentKey(path) ?? path, files);
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
  async writeNew(path: FileKey, content: string, c: Confirm) {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) throw new Error(`Not a file: ${path}`);
    const { machineId, abs } = splitKey(k);
    await fsApi.mkdir(machineId, dirOf(abs), c).catch(() => undefined);
    return fsApi.write(machineId, { path: abs, content, projectId: this.partFor(k).ref, ...c });
  }

  async duplicate(path: FileKey) {
    const k = this.toKey(path);
    if (!k || isRootKey(k)) return;
    const { machineId, abs } = splitKey(k);
    const r = await api.fsRead(machineId, abs, MAX_VIEW + 1);
    if (r.binary) {
      toast.error('Binary files cannot be duplicated here.');
      return;
    }
    const st = await this.run((c) => fsApi.write(machineId, { path: abs, content: r.content, unique: true, projectId: this.partFor(k).ref, ...c }));
    if (!st) return;
    const parent = parentKey(k);
    if (parent) void this.loadDir(parent);
    this.partOf(k)?.filesChanged();
    this.open(fileKey(machineId, st.path), { pin: true });
  }
}
