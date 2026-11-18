// The editor core of the code view (workspace.ts): tool tabs, the split grid of up to four
// groups, consoles as tabs (terminals moved out of the panel and back, a board's agents), and
// what of it a reload keeps, with the row of before read onto the grid.
// The hub, Monaco and the language servers are fakes, as in tabs.test.ts.

import { beforeEach, describe, expect, it, vi } from 'vitest';
// @ts-expect-error: Monaco's internal module has no types of its own here
import { URI } from 'monaco-editor/base/common/uri.js';

const saved = new Map<string, string>();
const files = new Map<string, { content: string; mtime: string }>();

class FakeModel {
  alt = 1;
  private subs = new Set<() => void>();
  constructor(
    private value: string,
    readonly uri: URI,
  ) {}
  getValue() {
    return this.value;
  }
  setValue(v: string) {
    this.value = v;
    this.alt++;
    for (const fn of [...this.subs]) fn();
  }
  getEOL() {
    return '\n';
  }
  pushStackElement() {}
  getFullModelRange() {
    return null;
  }
  pushEditOperations(_sel: unknown, ops: { text: string }[]) {
    this.setValue(ops[0].text);
    return null;
  }
  getAlternativeVersionId() {
    return this.alt;
  }
  onDidChangeContent(fn: () => void) {
    this.subs.add(fn);
    return { dispose: () => this.subs.delete(fn) };
  }
  dispose() {
    models.delete(this.uri.toString());
  }
}
const models = new Map<string, FakeModel>();

vi.mock('./monaco', () => ({
  monaco: {
    Uri: URI,
    editor: {
      getModel: (u: URI) => models.get(u.toString()) ?? null,
      createModel: (text: string, _l: unknown, uri: URI) => {
        const m = new FakeModel(text, uri);
        models.set(uri.toString(), m);
        return m;
      },
      getEditors: () => [],
      getModels: () => [...models.values()],
      onDidChangeMarkers: () => ({ dispose() {} }),
      getModelMarkers: () => [],
    },
  },
  languageFor: () => 'plaintext',
}));
vi.mock('./lsp', () => ({
  hubOf: () => null,
  LspHub: class {
    servers = [];
    constructor(readonly host: unknown) {}
    start() {}
    resume() {}
    dispose() {}
    attach() {}
    detach() {}
    saved() {}
    closedDiagnostics() {
      return [];
    }
    workspaceSymbols() {
      return Promise.resolve([]);
    }
  },
}));
vi.mock('../../api', () => ({
  api: {
    fsStat: async (m: string, p: string) => {
      const f = files.get(`${m}:${p}`);
      return f ? { exists: true, size: f.content.length, mtime: f.mtime } : { exists: false, size: 0, mtime: '' };
    },
    fsRead: async (m: string, p: string) => {
      const f = files.get(`${m}:${p}`);
      if (!f) throw new Error('no such file');
      return { content: f.content, size: f.content.length, mtime: f.mtime, binary: false };
    },
  },
  ApiError: class extends Error {},
  errorMessage: (e: unknown) => String(e),
  fsRawUrl: () => '',
  isCode: () => false,
  wsUrl: (p: string) => p,
}));
vi.mock('../api', () => ({
  filesApi: {
    list: async () => ({ items: [], truncated: false, source: 'git' }),
    ignored: async () => ({ items: [] }),
    onSave: async () => ({ runs: [] }),
    dapStatus: async () => ({ items: [] }),
  },
  fsApi: {
    tree: async (_m: string, p: string) => ({ path: p, entries: [], truncated: false }),
    watch: async () => ({ watchId: 'w1', stats: [], intervalMs: 3000 }),
    unwatch: async () => ({}),
    renewWatch: async () => ({}),
    trash: async (_m: string, p: string) => ({ trashedTo: `/home/me/shop/.git/nextboard-trash/1${p.slice('/home/me/shop'.length)}` }),
  },
  gitApi: { status: async () => ({ isRepo: false, files: [] }) },
  laravelApi: { index: async () => ({ detected: false }) },
  reviewApi: { list: async () => ({ supported: false, agents: [] }) },
}));
vi.mock('../store', () => ({
  studioStore: {
    get: () => ({ reviewRev: {} }),
    subscribe: () => () => {},
    onReviewOverlap: new Set(),
    gitTarget: (pid: string) => ({ pid, repo: '' }),
    loadGitRepos: async () => {},
    watchFs: () => () => {},
  },
  waitJob: async () => ({}),
}));
vi.mock('../drafts', () => ({ drafts: { get: async () => undefined, put: async () => undefined, remove: async () => undefined } }));
const sessions: Record<string, Partial<import('../../lib/types').Session>> = {};
vi.mock('../../store', () => ({ store: { getState: () => ({ settings: {}, sessions }) } }));
vi.mock('../../components/Toast', () => ({ toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), notify: vi.fn() } }));
vi.mock('../../lib/format', () => ({ prefs: { get: (k: string) => saved.get(k) ?? null, set: (k: string, v: string) => saved.set(k, v) } }));
vi.mock('../../board/files', () => ({ sendPasted: async () => [] }));
vi.mock('../../lib/pack', () => ({ packUpload: async () => ({}) }));

vi.stubGlobal('window', Object.assign(globalThis, { localStorage: { removeItem: (k: string) => saved.delete(k.replace(/^nb\./, '')) } }));

const { Workspace } = await import('./workspace');
const { fileKey } = await import('./paths');
const { toast } = await import('../../components/Toast');
type Project = import('../../lib/types').Project;
type Ws = InstanceType<typeof Workspace>;

const settle = async () => {
  for (let i = 0; i < 8; i++) await new Promise((r) => setTimeout(r, 0));
};

let seq = 0;
const project = (parts: NonNullable<Project['parts']>): Project => ({
  id: `p_tabs${++seq}`,
  name: 'Shop',
  machineId: parts[0].machineId,
  path: parts[0].path,
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});
const onePart = () => project([{ id: 'pt_main', name: 'Main', machineId: 'local', path: '/home/me/shop' }]);

const ROOT = '/home/me/shop';
const names = (ws: Ws, gi = 0) => ws.groups[gi].tabs.map((t) => (t.pane ? `pane:${t.pane}` : t.path.slice(ROOT.length + 1)));
const tab = (ws: Ws, name: string, gi = 0) => ws.groups[gi].tabs.find((t) => t.path === `${ROOT}/${name}`)!;
const persist = (ws: Ws) => (ws as unknown as { persist(): void }).persist();

/** A card with these files open, kept (not previews), in this order. */
function card(...open: string[]): { p: Project; ws: Ws } {
  const p = onePart();
  const ws = Workspace.get(p);
  for (const f of open) {
    files.set(`local:${ROOT}/${f}`, { content: f, mtime: 'm1' });
    ws.open(`${ROOT}/${f}`, { pin: true });
  }
  return { p, ws };
}

beforeEach(() => {
  saved.clear();
  files.clear();
  for (const k of Object.keys(sessions)) delete sessions[k];
  vi.mocked(toast.info).mockClear();
});

const ids = (ws: Ws) => ws.groups.map((g) => g.tabs.map((t) => (t.pane ? `pane:${t.pane}` : t.tool ? `tool:${t.tool.kind}${t.tool.arg ? `:${t.tool.arg}` : ''}` : t.path.slice(ROOT.length + 1))));

describe('tool tabs', () => {
  it('open once per kind in the active group, focus the open one, and are never previews', () => {
    const { ws } = card('a.ts');
    ws.openTool('vault');
    expect(ids(ws)).toEqual([['a.ts', 'tool:vault']]);
    expect(ws.activeTab?.tool).toEqual({ kind: 'vault' });
    expect(ws.activeTab?.preview).toBe(false);
    ws.activate(0, tab(ws, 'a.ts').id);
    ws.openTool('vault');
    expect(ids(ws)).toEqual([['a.ts', 'tool:vault']]);
    expect(ws.activeTab?.tool?.kind).toBe('vault');
  });

  it('a browser per address; the one open with that address is focused, wherever it is', () => {
    const { ws } = card('a.ts');
    ws.openTool('browser', 'http://localhost:5173/');
    ws.splitTab(0, tab(ws, 'a.ts').id, 'right');
    ws.openTool('browser', 'http://localhost:8000/');
    expect(ids(ws)).toEqual([['a.ts', 'tool:browser:http://localhost:5173/'], ['a.ts', 'tool:browser:http://localhost:8000/']]);
    ws.openTool('browser', 'http://localhost:5173/');
    expect(ws.activeGroup).toBe(0);
    expect(ws.activeTab?.tool?.arg).toBe('http://localhost:5173/');
  });

  it('a browser that went elsewhere keeps its tab (and its id); opening its old address opens another', () => {
    const { ws } = card();
    ws.openTool('browser', 'http://localhost:5173/');
    const id = ws.activeTab!.id;
    ws.setToolArg(id, 'http://localhost:5173/cart');
    expect(ws.activeTab).toMatchObject({ id, tool: { kind: 'browser', arg: 'http://localhost:5173/cart' } });
    ws.openTool('browser', 'http://localhost:5173/');
    expect(ws.groups[0].tabs).toHaveLength(2);
    expect(new Set(ws.groups[0].tabs.map((t) => t.id)).size).toBe(2);
  });

  it('come back after a reload, with their pin, lock and address, the active one too', () => {
    const { p, ws } = card('a.ts');
    ws.openTool('memory');
    ws.setLocked(0, ws.activeTab!.id);
    ws.openTool('browser', 'http://localhost:5173/');
    ws.setToolArg(ws.activeTab!.id, 'http://localhost:5173/cart');
    persist(ws);
    expect(ws.release()).toBe(true);
    const again = Workspace.get(p);
    expect(ids(again)).toEqual([['a.ts', 'tool:memory', 'tool:browser:http://localhost:5173/cart']]);
    expect(again.groups[0].tabs[1].locked).toBe(true);
    expect(again.activeTab?.tool).toEqual({ kind: 'browser', arg: 'http://localhost:5173/cart' });
  });

  it('split and move like any tab that has one place: alone in a group they stay', () => {
    const { ws } = card('a.ts');
    ws.openTool('chats');
    const id = ws.activeTab!.id;
    expect(ws.splitTab(0, id, 'bottom')).toBe(true);
    expect(ids(ws)).toEqual([['a.ts'], ['tool:chats']]);
    expect(ws.splitTab(1, id, 'right')).toBe(false);
    expect(ws.groups).toHaveLength(2);
  });
});

describe('the split grid', () => {
  it('Split Right and Split Down make a 2×2 grid of four groups, in reading order, kept over a reload', () => {
    const { p, ws } = card('a.ts');
    ws.splitRight();
    ws.splitDown();
    expect(ws.groups).toHaveLength(3);
    expect(ws.grid).toEqual({ dir: 'row', a: { g: ws.groups[0].id }, b: { dir: 'col', a: { g: ws.groups[1].id }, b: { g: ws.groups[2].id }, at: 0.5 }, at: 0.5 });
    ws.activate(0, ws.groups[0].tabs[0].id);
    ws.splitDown();
    expect(ws.groups).toHaveLength(4);
    // top left, bottom left, top right, bottom right
    expect(ws.activeGroup).toBe(1);
    expect(ws.groups.every((g) => g.tabs.length === 1)).toBe(true);
    ws.resizeSplit('', 0.62);
    const grid = JSON.stringify(ws.grid).replace(/"g\d+"/g, '"g"');
    persist(ws);
    expect(ws.release()).toBe(true);
    const again = Workspace.get(p);
    expect(again.groups).toHaveLength(4);
    expect(JSON.stringify(again.grid).replace(/"g\d+"/g, '"g"')).toBe(grid);
    expect(again.activeGroup).toBe(1);
  });

  it('a full grid that way: the group already there takes the file, else it says so', () => {
    const { ws } = card('a.ts', 'b.ts');
    ws.splitRight();
    expect(ws.groups).toHaveLength(2);
    // right of the right group: no third column; nothing there either
    ws.activate(1, ws.groups[1].tabs[0].id);
    ws.splitRight();
    expect(ws.groups).toHaveLength(2);
    expect(toast.info).toHaveBeenCalledWith('The editor holds four groups at most, two across and two down.');
    // left of it is the first group: it shows the file (already there)
    ws.split('left');
    expect(ws.activeGroup).toBe(0);
  });

  it('a tab dragged to an edge moves into a new group there; to its own group’s edge only when others stay', () => {
    const { ws } = card('a.ts', 'b.ts');
    ws.moveTabToEdge(0, tab(ws, 'b.ts').id, 0, 'bottom');
    expect(ids(ws)).toEqual([['a.ts'], ['b.ts']]);
    expect(ws.grid).toMatchObject({ dir: 'col' });
    expect(ws.activeGroup).toBe(1);
    // the only tab of a group, to its own edge: nothing happens
    ws.moveTabToEdge(1, tab(ws, 'b.ts', 1).id, 1, 'right');
    expect(ids(ws)).toEqual([['a.ts'], ['b.ts']]);
    // to the other group's left edge: its group empties and goes, the new one takes the left
    ws.moveTabToEdge(1, tab(ws, 'b.ts', 1).id, 0, 'left');
    expect(ids(ws)).toEqual([['b.ts'], ['a.ts']]);
    expect(ws.grid).toMatchObject({ dir: 'row' });
  });

  it('an edge it cannot split at: the tab joins that group', () => {
    const { ws } = card('a.ts', 'b.ts', 'c.ts');
    ws.splitTab(0, tab(ws, 'a.ts').id, 'right');
    ws.moveTabToEdge(0, tab(ws, 'c.ts').id, 1, 'right');
    expect(ids(ws)).toEqual([['a.ts', 'b.ts'], ['a.ts', 'c.ts']]);
  });

  it('a file dropped at an edge opens in a new group there', () => {
    const { ws } = card('a.ts');
    files.set(`local:${ROOT}/n.ts`, { content: 'n', mtime: 'm1' });
    ws.openAtEdge(`${ROOT}/n.ts`, 0, 'top');
    expect(ids(ws)).toEqual([['n.ts'], ['a.ts']]);
    expect(ws.activeGroup).toBe(0);
  });

  it('closing a group’s last tab gives its place to its neighbour; the active group stays the same group', () => {
    const { ws } = card('a.ts');
    ws.splitRight();
    ws.splitDown();
    const [left, , bottom] = ws.groups;
    ws.activate(0, left.tabs[0].id);
    ws.closeTab(2, bottom.tabs[0].id);
    expect(ws.groups).toHaveLength(2);
    expect(ws.grid).toEqual({ dir: 'row', a: { g: left.id }, b: { g: ws.groups[1].id }, at: 0.5 });
    expect(ws.group).toBe(left);
  });

  it('sizes are dragged within 10–90 % and kept, the layout comes back after a reload', () => {
    const { p, ws } = card('a.ts', 'b.ts');
    ws.splitTab(0, tab(ws, 'b.ts').id, 'bottom');
    ws.activate(0, tab(ws, 'a.ts').id);
    ws.splitRight();
    ws.resizeSplit('', 0.7);
    ws.resizeSplit('a', 2);
    persist(ws);
    const raw = JSON.parse(saved.get(`wb.${p.id}`)!) as { grid: unknown; groups: unknown[] };
    expect(raw.grid).toEqual({ v: 2, root: { dir: 'col', a: { dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.9 }, b: { g: 2 }, at: 0.7 } });
    expect(raw.groups).toHaveLength(3);
    expect(ws.release()).toBe(true);
    const again = Workspace.get(p);
    expect(ids(again)).toEqual([['a.ts', 'b.ts'], ['a.ts'], ['b.ts']]);
    expect(again.grid).toEqual({ dir: 'col', a: { dir: 'row', a: { g: again.groups[0].id }, b: { g: again.groups[1].id }, at: 0.9 }, b: { g: again.groups[2].id }, at: 0.7 });
    expect(again.activeGroup).toBe(1);
  });

  it('a state from before the grid (one row of three) opens on it: two side by side, the third under the second', () => {
    const q = onePart();
    saved.set(
      `wb.${q.id}`,
      JSON.stringify({
        v: 2,
        groups: ['a.ts', 'b.ts', 'c.ts'].map((f) => ({ tabs: [{ path: `${ROOT}/${f}`, preview: false }], active: `${ROOT}/${f}` })),
        activeGroup: 2,
      }),
    );
    const ws = Workspace.get(q);
    expect(ids(ws)).toEqual([['a.ts'], ['b.ts'], ['c.ts']]);
    expect(ws.grid).toEqual({ dir: 'row', a: { g: ws.groups[0].id }, b: { dir: 'col', a: { g: ws.groups[1].id }, b: { g: ws.groups[2].id }, at: 0.5 }, at: 0.5 });
    expect(ws.activeGroup).toBe(2);
    // and a fourth group fits now
    ws.activate(0, ws.groups[0].tabs[0].id);
    ws.splitDown();
    expect(ws.groups).toHaveLength(4);
  });
});

describe('after a reload', () => {
  it('a browser tab whose address another tab took first keeps its group showing it', () => {
    const { p, ws } = card('a.ts');
    ws.openTool('browser', 'http://localhost:5173/');
    ws.splitTab(0, tab(ws, 'a.ts').id, 'right');
    ws.openTool('browser', 'http://localhost:8000/');
    // the right one goes to the left one's address: both save "tool:browser:http://localhost:5173/"
    ws.setToolArg(ws.activeTab!.id, 'http://localhost:5173/');
    persist(ws);
    expect(ws.release()).toBe(true);
    const again = Workspace.get(p);
    const right = again.groups[1];
    expect(right.tabs.map((t) => t.id)).toContain(right.active);
    expect(right.tabs.find((t) => t.id === right.active)?.tool).toEqual({ kind: 'browser', arg: 'http://localhost:5173/' });
  });

  it('an active id that names no tab shows the group’s first tab', () => {
    const q = onePart();
    saved.set(`wb.${q.id}`, JSON.stringify({ v: 2, groups: [{ tabs: [{ path: `${ROOT}/a.ts`, preview: false }, { path: '', preview: false, tool: { kind: 'chats' } }], active: 'tool:memory' }] }));
    const ws = Workspace.get(q);
    expect(ws.groups[0].active).toBe(`${ROOT}/a.ts`);
  });
});

describe('saving', () => {
  it('waits 400 ms after a change, and a page put away writes what waits at once', () => {
    const { p, ws } = card('a.ts');
    ws.openTool('vault');
    ws.flushSave();
    const raw = JSON.parse(saved.get(`wb.${p.id}`)!) as { groups: { tabs: { tool?: unknown }[] }[] };
    expect(raw.groups[0].tabs[1].tool).toEqual({ kind: 'vault' });
    // nothing waits: a second flush writes nothing new
    saved.delete(`wb.${p.id}`);
    ws.flushSave();
    expect(saved.has(`wb.${p.id}`)).toBe(false);
  });
});

describe('consoles as tabs', () => {
  it('a terminal moves from the panel to the editor area and back, the same session all along', () => {
    const { ws } = card('a.ts');
    sessions.t1 = { id: 't1', panel: true, agentKind: 'shell', name: 'Terminal' };
    ws.terminals = ['t1', 't2'];
    ws.activeTerminal = 't1';
    ws.movePanelTerminalToEditor('t1');
    expect(ws.terminals).toEqual(['t2']);
    expect(ws.activeTerminal).toBe('t2');
    expect(ids(ws)).toEqual([['a.ts', 'pane:t1']]);
    expect(ws.activeTab?.pane).toBe('t1');
    ws.movePaneToPanel('t1');
    expect(ids(ws)).toEqual([['a.ts']]);
    expect(ws.terminals).toEqual(['t2', 't1']);
    expect(ws).toMatchObject({ activeTerminal: 't1', panelOpen: true, panelView: 'terminal' });
  });

  it('a terminal’s tab asks before it is killed; a locked one stays where it is', () => {
    const { ws } = card();
    sessions.t1 = { id: 't1', panel: true, agentKind: 'shell' };
    ws.openPane('t1');
    const id = ws.paneTabAt('t1')!.tab.id;
    ws.closeTab(0, id);
    expect(ws.closingPane).toBe('t1');
    ws.closingPane = '';
    ws.setLocked(0, id);
    ws.movePaneToPanel('t1');
    expect(ws.paneTabAt('t1')).not.toBeNull();
    expect(ws.terminals).toEqual([]);
  });

  it('a board’s agent opens as a tab, once; closing the tab never asks to stop it', () => {
    const { ws } = card('a.ts');
    sessions.b1 = { id: 'b1', agentKind: 'agent', status: 'asleep' };
    ws.openPane('b1');
    ws.splitTab(0, tab(ws, 'a.ts').id, 'right');
    ws.openPane('b1');
    expect(ids(ws)).toEqual([['a.ts', 'pane:b1'], ['a.ts']]);
    expect(ws.activeGroup).toBe(0);
    ws.closeTab(0, ws.paneTabAt('b1')!.tab.id);
    expect(ws.closingPane).toBe('');
    expect(ws.paneTabAt('b1')).toBeNull();
  });

  it('a board’s agent is marked on its tab and saved so: closed before the browser knows the session again, it is still not stopped', () => {
    const { p, ws } = card('a.ts');
    sessions.b2 = { id: 'b2', agentKind: 'agent' };
    ws.openPane('b2');
    expect(ws.paneTabAt('b2')!.tab.board).toBe(true);
    persist(ws);
    const raw = JSON.parse(saved.get(`wb.${p.id}`)!) as { groups: { tabs: Record<string, unknown>[] }[] };
    expect(raw.groups[0].tabs[1]).toEqual({ path: '', preview: false, pane: 'b2', board: true });
    expect(ws.release()).toBe(true);
    delete sessions.b2; // just after a reload: the store has not seen it yet
    const again = Workspace.get(p);
    expect(again.paneTabAt('b2')!.tab.board).toBe(true);
    again.closeTab(0, again.paneTabAt('b2')!.tab.id);
    expect(again.closingPane).toBe('');
    expect(again.paneTabAt('b2')).toBeNull();
  });

  it('a console the browser does not know is never offered to stop: its tab just closes', () => {
    const { ws } = card();
    ws.openPane('ghost');
    ws.closeTab(0, ws.paneTabAt('ghost')!.tab.id);
    expect(ws.closingPane).toBe('');
    expect(ws.paneTabAt('ghost')).toBeNull();
  });

  it('the panel starts a terminal by itself only while the view never had one, also over a reload', () => {
    const { p, ws } = card();
    expect(ws.hadTerminal).toBe(false);
    sessions.t3 = { id: 't3', panel: true, agentKind: 'shell' };
    ws.terminals = ['t3'];
    ws.movePanelTerminalToEditor('t3');
    expect(ws.terminals).toEqual([]);
    expect(ws.hadTerminal).toBe(true);
    persist(ws);
    expect(ws.release()).toBe(true);
    expect(Workspace.get(p).hadTerminal).toBe(true);
    // a state from before the flag that had terminals has had one
    const q = onePart();
    saved.set(`wb.${q.id}`, JSON.stringify({ v: 2, terminals: ['t9'] }));
    expect(Workspace.get(q).hadTerminal).toBe(true);
  });

  it('openPane takes a pane out of the side bar into the group asked for', () => {
    const { ws } = card('a.ts');
    ws.splitTab(0, tab(ws, 'a.ts').id, 'bottom');
    ws.placePane('ses_9', 'side');
    expect(ws.rightTab).toBe('pane');
    ws.openPane('ses_9', { group: 0 });
    expect(ws.rightPanes).toEqual([]);
    expect(ws.rightTab).toBe('lead');
    expect(ids(ws)).toEqual([['a.ts', 'pane:ses_9'], ['a.ts']]);
  });
});
