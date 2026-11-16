// What a Code workbench card keeps in the browser (wb.<viewKey>, wb.bookmarks.<viewKey>,
// wb.debug.<viewKey>) and how older saved state is read (v0.9.0-beta.2, docs/API-studio.md §7).
// Pure: no Monaco, no DOM, no API calls.
//
// From v: 2 every path in it is a FileKey (paths.ts). Without a version the paths are absolute on
// the machine of the part the state was saved for: on This PC a key is the path itself, so a
// single-part local project reads today's state as is.

import { asFileKey, cleanAbs, fileKey, keyUnder, PERSIST_VERSION, storedPaths, type FileKey } from './paths';

export type SideView = 'files' | 'search' | 'scm' | 'review' | 'debug' | 'laravel' | 'extensions' | 'agents' | 'settings';
export type PanelView = 'terminal' | 'problems' | 'output' | 'debug' | 'tasks' | 'ports';
export type MdView = 'reader' | 'split' | 'source';

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

export const MAX_GROUPS = 3;
export const MAX_RECENT = 50;
/** The most tabs the first Root open takes over from the part states. */
export const MAX_MERGED_TABS = 40;

export interface PersistedTab {
  /** A key ("" for a pane). */
  path: string;
  preview: boolean;
  pane?: string;
  /** Saved only when set: a state from before pins and locks reads as neither. */
  pinned?: boolean;
  locked?: boolean;
}

/** The pin and the lock of a saved tab, only those that are set. */
const flagsOf = (t: Partial<PersistedTab>): Pick<PersistedTab, 'pinned' | 'locked'> => ({ ...(t.pinned === true ? { pinned: true } : {}), ...(t.locked === true ? { locked: true } : {}) });

export interface Persisted {
  /** PERSIST_VERSION: the paths are FileKeys. */
  v?: number;
  groups: { tabs: PersistedTab[]; active: string | null }[];
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

/** The key of a saved tab id: panes stay as they are, a diff (never saved) goes. */
function readTabId(raw: string | null | undefined, read: (raw: string) => FileKey | null): string | null {
  if (!raw) return null;
  if (raw.startsWith(PANE)) return raw;
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
      if (t.pane) return [{ path: '', preview: false, pane: t.pane, ...flagsOf(t) }];
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
  return [...(s.groups ?? []).flatMap((g) => g.tabs.flatMap((t) => (t.pane ? [] : [t.path as FileKey]))), ...((s.expanded ?? []) as FileKey[])];
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
  const tabId = (t: PersistedTab) => (t.pane ? `${PANE}${t.pane}` : t.path);
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
  return { ...base, groups, activeGroup: Math.min(base.activeGroup ?? 0, groups.length - 1), expanded, recent, md, terminals, rightPanes };
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
