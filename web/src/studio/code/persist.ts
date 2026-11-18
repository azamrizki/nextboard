// What a Code workbench card keeps in the browser (wb.<viewKey>, wb.bookmarks.<viewKey>,
// wb.debug.<viewKey>) and how older saved state is read (v0.9.0-beta.2, docs/API-studio.md §7).
// Pure: no Monaco, no DOM, no API calls.
//
// From v: 2 every path in it is a FileKey (paths.ts). Without a version the paths are absolute on
// the machine of the part the state was saved for: on This PC a key is the path itself, so a
// single-part local project reads today's state as is.
//
// The groups' layout (grid.ts) is kept apart from the paths' version: `grid: { v: 2, root }`.
// A state without it is from the days of one row of at most three groups (layout v1), and is read
// as that row on the grid (readLayout).

import { fromRow, fromSaved, leaves, prune, readSaved, toSaved, type SavedGrid } from './grid';
import { asFileKey, cleanAbs, fileKey, keyUnder, PERSIST_VERSION, storedPaths, type FileKey } from './paths';
import { isToolKind, type ToolKind } from './toolKinds';

export type SideView = 'files' | 'search' | 'scm' | 'review' | 'debug' | 'laravel' | 'extensions' | 'agents' | 'settings';
export type PanelView = 'terminal' | 'problems' | 'output' | 'debug' | 'tasks' | 'ports';
export type MdView = 'reader' | 'split' | 'source';
/** The code view's right side bar: the lead, the Agents list, or a pane put there. */
export type RightTab = 'lead' | 'agents' | 'pane';

export interface SearchState {
  query: string;
  replace: string;
  matchCase: boolean;
  wholeWord: boolean;
  regex: boolean;
  include: string;
  exclude: string;
}

export const DEFAULT_SEARCH: SearchState = { query: '', replace: '', matchCase: false, wholeWord: false, regex: false, include: '', exclude: '' };

/** Editor groups: four at most, two by two (grid.ts). */
export const MAX_GROUPS = 4;
/** The version of the saved groups' layout: 2 is the grid; none is the row before it. */
export const LAYOUT_VERSION = 2;
export const MAX_RECENT = 50;
/** The most tabs the first Root open takes over from the part states. */
export const MAX_MERGED_TABS = 40;

export interface PersistedTab {
  /** A key ("" for a pane or a tool). */
  path: string;
  preview: boolean;
  pane?: string;
  /** A tool of the code view (toolKinds.ts), with its argument (a browser's address). */
  tool?: { kind: ToolKind; arg?: string };
  /** Of a pane: a board's console shown in the editor (its close never stops it). */
  board?: boolean;
  /** Saved only when set: a state from before pins and locks reads as neither. */
  pinned?: boolean;
  locked?: boolean;
}

export interface PersistedGroup {
  tabs: PersistedTab[];
  active: string | null;
}

/** The pin and the lock of a saved tab, only those that are set. */
const flagsOf = (t: Partial<PersistedTab>): Pick<PersistedTab, 'pinned' | 'locked'> => ({ ...(t.pinned === true ? { pinned: true } : {}), ...(t.locked === true ? { locked: true } : {}) });

export interface Persisted {
  /** PERSIST_VERSION: the paths are FileKeys. */
  v?: number;
  groups: PersistedGroup[];
  activeGroup: number;
  /** Where the groups lie (LAYOUT_VERSION): leaves are indices into `groups`. */
  grid?: { v: number; root: SavedGrid };
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
  rightTab?: RightTab;
  /** The view has had a terminal: the panel no longer starts one by itself. */
  hadTerminal?: boolean;
  /** Root card: the part the Debug view runs in (an id). */
  debugPart?: string;
}

/** Saved state read for a view: every path a FileKey, what named none dropped. */
export type ReadState = Partial<Persisted> & { v: number };

export interface SavedBookmark {
  path: string;
  line: number;
}

export interface SavedBreakpoint {
  id: number;
  path: string;
  line: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  logMessage?: string;
}

export interface SavedDebug {
  v?: number;
  breakpoints: SavedBreakpoint[];
  watches: string[];
  config: string;
  filters: string[];
  /** Root card: the part the debugger runs in. */
  part?: string;
}

const PANE = 'pane:';
const TOOL = 'tool:';

/** The id of a tool's tab: one per kind, a browser one per address (workspace.ts openTool). */
export const toolTabId = (kind: ToolKind, arg?: string) => (kind === 'browser' ? `${TOOL}${kind}:${arg ?? ''}` : `${TOOL}${kind}`);

/** The key of a saved tab id: panes and tools stay as they are, a diff (never saved) goes. */
function readTabId(raw: string | null | undefined, read: (raw: string) => FileKey | null): string | null {
  if (!raw) return null;
  if (raw.startsWith(PANE) || raw.startsWith(TOOL)) return raw;
  if (raw.startsWith('diff:')) return null;
  return read(raw);
}

/**
 * Saved state as a view reads it: v: 2 paths are keys, an older state's paths are absolute on
 * legacyMachineId (paths.ts storedPaths). Entries that name no file are dropped.
 */
export function readState(saved: Partial<Persisted> | null | undefined, legacyMachineId: string): ReadState {
  const s = saved ?? {};
  const read = storedPaths(s, legacyMachineId);
  const keys = (list: unknown) => (Array.isArray(list) ? list.flatMap((p) => (typeof p === 'string' ? (read(p) ?? []) : [])) : []);
  const groups = (Array.isArray(s.groups) ? s.groups : []).map((g) => ({
    tabs: (Array.isArray(g?.tabs) ? g.tabs : []).flatMap((t): PersistedTab[] => {
      if (!t || typeof t !== 'object') return [];
      if (t.pane) return [{ path: '', preview: false, pane: t.pane, ...(t.board === true ? { board: true } : {}), ...flagsOf(t) }];
      if (t.tool) return isToolKind(t.tool.kind) ? [{ path: '', preview: false, tool: { kind: t.tool.kind, ...(typeof t.tool.arg === 'string' && t.tool.arg ? { arg: t.tool.arg } : {}) }, ...flagsOf(t) }] : [];
      const k = typeof t.path === 'string' ? read(t.path) : null;
      return k ? [{ path: k, preview: !!t.preview, ...flagsOf(t) }] : [];
    }),
    active: readTabId(g?.active, read),
  }));
  const md: Record<string, MdView> = {};
  for (const [p, v] of Object.entries(s.md ?? {})) {
    const k = read(p);
    if (k) md[k] = v;
  }
  return { ...s, v: PERSIST_VERSION, groups, expanded: keys(s.expanded), recent: keys(s.recent), md };
}

/** Bookmarks or breakpoints saved without a version: "//m-…" keys stay keys, a plain path is on machineId. */
export function readAnyPath(raw: string, machineId: string): FileKey | null {
  if (raw.startsWith('//')) return asFileKey(raw);
  const p = cleanAbs(raw);
  return p === null ? null : fileKey(machineId, p);
}

export function readBookmarks(saved: unknown, machineId: string): SavedBookmark[] {
  if (!Array.isArray(saved)) return [];
  return saved.flatMap((b) => {
    const k = b && typeof b.path === 'string' && typeof b.line === 'number' ? readAnyPath(b.path, machineId) : null;
    return k ? [{ path: k, line: b.line }] : [];
  });
}

export function readDebug(saved: Partial<SavedDebug> | null | undefined, machineId: string): SavedDebug {
  const s = saved ?? {};
  const read = s.v === PERSIST_VERSION ? (raw: string) => asFileKey(raw) : (raw: string) => readAnyPath(raw, machineId);
  const breakpoints = (Array.isArray(s.breakpoints) ? s.breakpoints : []).flatMap((b) => {
    const k = b && typeof b.path === 'string' ? read(b.path) : null;
    return k ? [{ ...b, path: k }] : [];
  });
  return { v: PERSIST_VERSION, breakpoints, watches: Array.isArray(s.watches) ? s.watches : [], config: s.config ?? '', filters: Array.isArray(s.filters) ? s.filters : [], part: s.part };
}

/** Every file path a state names (tabs, then expanded folders), as keys. */
function pathsOf(s: ReadState): FileKey[] {
  return [...(s.groups ?? []).flatMap((g) => g.tabs.flatMap((t) => (t.pane || t.tool || !t.path ? [] : [t.path as FileKey]))), ...((s.expanded ?? []) as FileKey[])];
}

/**
 * The groups a view opens with, and where they lie: the groups with tabs (four at most), in the
 * layout's reading order, the layout over them, and the index of the active one. A state without
 * a layout (v1: one row) puts its groups on the grid as fromRow does; a layout that does not
 * match its groups is read the same way. Null when no group has a tab.
 */
export function readLayout(s: Pick<Partial<Persisted>, 'groups' | 'grid' | 'activeGroup'>): { groups: PersistedGroup[]; grid: SavedGrid; activeGroup: number } | null {
  const all = Array.isArray(s.groups) ? s.groups : [];
  const ids = all.map((_, i) => String(i));
  const withTabs = new Set(ids.filter((_, i) => all[i]?.tabs?.length));
  if (!withTabs.size) return null;
  const saved = s.grid && s.grid.v === LAYOUT_VERSION ? readSaved(s.grid.root, all.length) : null;
  const tree = saved ? prune(fromSaved(saved, ids), withTabs) : fromRow([...withTabs].slice(0, MAX_GROUPS));
  if (!tree) return null;
  const order = leaves(tree);
  const groups = order.map((i) => all[Number(i)]);
  const active = order.indexOf(String(s.activeGroup ?? 0));
  return { groups, grid: toSaved(tree, order), activeGroup: Math.max(0, active) };
}

/**
 * Whether an old project-level state (wb.<projectId>, saved when the editor showed the first
 * part) belongs to a part: what it names lies in that part's folder. Read it with the part's
 * machine first (readState(saved, part machine)), then ask with the part's root key. A state
 * that names nothing belongs to no part.
 */
export function stateUnder(s: ReadState, rootKey: FileKey): boolean {
  const paths = pathsOf(s);
  return paths.length > 0 && paths.every((k) => keyUnder(k, rootKey));
}

/** Bookmarks or breakpoints all under a root (the same adoption rule as stateUnder). */
export function listUnder(list: { path: string }[], rootKey: FileKey): boolean {
  return list.length > 0 && list.every((b) => keyUnder(b.path as FileKey, rootKey));
}

const dedupe = <T>(list: T[], id: (x: T) => string) => {
  const seen = new Set<string>();
  return list.filter((x) => {
    const k = id(x);
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
};

/**
 * The first Root open of a multi-part project: one state from the part states that were saved
 * before (wb.<projectId> and every wb.<projectId>~<part>), each already read with its own
 * part's machine (readState). The layout and groups come from the first state with tabs; the
 * other states' tabs join group 1, pinned (deduplicated, capped). Expanded folders are combined,
 * plus the part roots. Recent files are combined in order, and so are the Markdown views.
 */
export function mergeLegacy(states: ReadState[], roots: FileKey[]): ReadState {
  const withTabs = states.find((s) => (s.groups ?? []).some((g) => g.tabs.length));
  const base: ReadState = { ...(withTabs ?? states[0] ?? {}), v: PERSIST_VERSION };
  const groups = (base.groups ?? []).filter((g) => g.tabs.length).slice(0, MAX_GROUPS).map((g) => ({ tabs: [...g.tabs], active: g.active }));
  if (!groups.length) groups.push({ tabs: [], active: null });
  const tabId = (t: PersistedTab) => (t.pane ? `${PANE}${t.pane}` : t.tool ? toolTabId(t.tool.kind, t.tool.arg) : t.path);
  const taken = new Set(groups.flatMap((g) => g.tabs.map(tabId)));
  let count = taken.size;
  for (const s of states) {
    if (s === withTabs) continue;
    for (const g of s.groups ?? []) {
      for (const t of g.tabs) {
        const id = tabId(t);
        if (taken.has(id) || count >= MAX_MERGED_TABS) continue;
        taken.add(id);
        count++;
        groups[0].tabs.push({ ...t, preview: false });
      }
    }
  }
  if (groups[0].active === null && groups[0].tabs.length) groups[0].active = tabId(groups[0].tabs[0]);
  const expanded = dedupe([...states.flatMap((s) => s.expanded ?? []), ...roots], (p) => p);
  const recent = dedupe(
    states.flatMap((s) => s.recent ?? []),
    (p) => p,
  ).slice(0, MAX_RECENT);
  const md: Record<string, MdView> = {};
  for (const s of states) Object.assign(md, s.md ?? {});
  const terminals = dedupe(
    states.flatMap((s) => s.terminals ?? []),
    (t) => t,
  );
  const rightPanes = dedupe(
    states.flatMap((s) => s.rightPanes ?? []),
    (t) => t,
  );
  // the groups were filtered and others' tabs joined the first: a saved layout no longer matches them
  return { ...base, groups, grid: undefined, activeGroup: Math.min(base.activeGroup ?? 0, groups.length - 1), expanded, recent, md, terminals, rightPanes };
}

/** Bookmarks of several old states as one list (deduplicated). */
export function mergeBookmarks(lists: SavedBookmark[][]): SavedBookmark[] {
  return dedupe(lists.flat(), (b) => `${b.path}:${b.line}`);
}

/** Debug states of several old states as one: breakpoints and watches combined, ids made unique. */
export function mergeDebug(list: SavedDebug[]): SavedDebug {
  let id = 1;
  const breakpoints = dedupe(list.flatMap((d) => d.breakpoints), (b) => `${b.path}:${b.line}`).map((b) => ({ ...b, id: id++ }));
  const watches = dedupe(list.flatMap((d) => d.watches), (w) => w);
  const filters = dedupe(list.flatMap((d) => d.filters), (f) => f);
  return { v: PERSIST_VERSION, breakpoints, watches, config: list.find((d) => d.config)?.config ?? '', filters };
}
