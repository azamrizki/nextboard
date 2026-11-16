// Pinned and locked tabs of an editor card (workspace.ts): pinned ones lead their strip and
// bulk closes leave them, nothing closes a locked one, and both flags survive a reload.
// The hub, Monaco and the language servers are fakes, as in workspace.test.ts.

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
vi.mock('../../store', () => ({ store: { getState: () => ({ settings: {} }) } }));
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
  vi.mocked(toast.info).mockClear();
});

describe('pinned tabs', () => {
  it('lead the strip in the order they were pinned; unpinned, a tab leads the unpinned ones', () => {
    const { ws } = card('a.ts', 'b.ts', 'c.ts');
    expect(names(ws)).toEqual(['a.ts', 'b.ts', 'c.ts']);
    ws.setPinned(0, tab(ws, 'c.ts').id);
    expect(names(ws)).toEqual(['c.ts', 'a.ts', 'b.ts']);
    ws.setPinned(0, tab(ws, 'b.ts').id, true);
    expect(names(ws)).toEqual(['c.ts', 'b.ts', 'a.ts']);
    // pinning again changes nothing
    ws.setPinned(0, tab(ws, 'c.ts').id, true);
    expect(names(ws)).toEqual(['c.ts', 'b.ts', 'a.ts']);
    ws.setPinned(0, tab(ws, 'c.ts').id, false);
    expect(names(ws)).toEqual(['b.ts', 'c.ts', 'a.ts']);
    expect(tab(ws, 'c.ts').pinned).toBeUndefined();
  });

  it('a new tab opened from a pinned one goes after the pinned ones', () => {
    const { ws } = card('a.ts', 'b.ts');
    ws.setPinned(0, tab(ws, 'b.ts').id);
    ws.activate(0, tab(ws, 'b.ts').id);
    files.set(`local:${ROOT}/d.ts`, { content: 'd', mtime: 'm1' });
    ws.open(`${ROOT}/d.ts`, { pin: true });
    expect(names(ws)).toEqual(['b.ts', 'd.ts', 'a.ts']);
    ws.activate(0, tab(ws, 'b.ts').id);
    ws.openDiff(fileKey('local', `${ROOT}/b.ts`), 'head');
    expect(ws.groups[0].tabs[1].id).toBe(`diff:head:${ROOT}/b.ts`);
  });

  it('pinning a preview tab makes it a kept tab, and the next preview opens beside it', () => {
    const { ws } = card('a.ts');
    files.set(`local:${ROOT}/p.ts`, { content: 'p', mtime: 'm1' });
    ws.open(`${ROOT}/p.ts`);
    expect(tab(ws, 'p.ts').preview).toBe(true);
    ws.setPinned(0, tab(ws, 'p.ts').id);
    expect(tab(ws, 'p.ts')).toMatchObject({ preview: false, pinned: true });
    expect(names(ws)).toEqual(['p.ts', 'a.ts']);
    files.set(`local:${ROOT}/q.ts`, { content: 'q', mtime: 'm1' });
    ws.open(`${ROOT}/q.ts`);
    expect(names(ws)).toEqual(['p.ts', 'q.ts', 'a.ts']);
    // a locked preview is kept too
    ws.setLocked(0, tab(ws, 'q.ts').id);
    expect(tab(ws, 'q.ts').preview).toBe(false);
  });

  it('Close Others, Close to the Right and Close All leave pinned and locked tabs; an explicit close takes a pinned one', () => {
    const { ws } = card('p.ts', 'a.ts', 'l.ts', 'b.ts', 'c.ts');
    ws.setPinned(0, tab(ws, 'p.ts').id);
    ws.setLocked(0, tab(ws, 'l.ts').id);
    ws.closeOthers(0, tab(ws, 'b.ts').id);
    expect(names(ws)).toEqual(['p.ts', 'l.ts', 'b.ts']);
    files.set(`local:${ROOT}/z.ts`, { content: 'z', mtime: 'm1' });
    ws.open(`${ROOT}/z.ts`, { pin: true });
    ws.closeRight(0, tab(ws, 'p.ts').id);
    expect(names(ws)).toEqual(['p.ts', 'l.ts']);
    files.set(`local:${ROOT}/y.ts`, { content: 'y', mtime: 'm1' });
    ws.open(`${ROOT}/y.ts`, { pin: true });
    ws.closeAll(0);
    expect(names(ws)).toEqual(['p.ts', 'l.ts']);
    ws.closeTab(0, tab(ws, 'p.ts').id);
    expect(names(ws)).toEqual(['l.ts']);
    expect(toast.info).not.toHaveBeenCalled();
  });
});

describe('locked tabs', () => {
  it('refuse an explicit close and say why; unlocked they close', () => {
    const { ws } = card('a.ts', 'l.ts');
    const id = tab(ws, 'l.ts').id;
    ws.setLocked(0, id);
    expect(names(ws)).toEqual(['a.ts', 'l.ts']); // a lock does not move a tab
    ws.closeTab(0, id);
    expect(names(ws)).toEqual(['a.ts', 'l.ts']);
    expect(toast.info).toHaveBeenCalledWith('Locked: unlock it to close.');
    ws.setLocked(0, id);
    expect(tab(ws, 'l.ts').locked).toBeUndefined();
    ws.closeTab(0, id);
    expect(names(ws)).toEqual(['a.ts']);
  });

  it('keep a split group open, and moving to another group keeps both flags', () => {
    const { ws } = card('a.ts', 'b.ts');
    ws.activate(0, tab(ws, 'a.ts').id);
    ws.splitRight();
    expect(ws.groups).toHaveLength(2);
    const right = ws.groups[1].tabs[0];
    ws.setLocked(1, right.id);
    ws.closeAll(1);
    expect(ws.groups).toHaveLength(2);
    // the left group's b.ts, pinned, moves to the right: it goes before the right's tabs
    ws.setPinned(0, tab(ws, 'b.ts').id);
    ws.moveTabToGroup(0, tab(ws, 'b.ts').id, 1);
    expect(names(ws, 1)).toEqual(['b.ts', 'a.ts']);
    expect(ws.groups[1].tabs[0]).toMatchObject({ pinned: true });
    expect(ws.groups[1].tabs[1]).toMatchObject({ locked: true });
  });

  it('an agent pane tab: no "stop the agent?" while locked, not moved to the side bar, kept by Close All', () => {
    const { ws } = card('a.ts');
    ws.placePane('ses_1', 'tab');
    const id = ws.paneTabAt('ses_1')!.tab.id;
    ws.setLocked(0, id);
    ws.closeTab(0, id);
    expect(ws.closingPane).toBe('');
    ws.askClosePane('ses_1');
    expect(ws.closingPane).toBe('');
    expect(toast.info).toHaveBeenCalledTimes(2);
    ws.placePane('ses_1', 'side');
    expect(ws.rightPanes).toEqual([]);
    expect(ws.paneTabAt('ses_1')).not.toBeNull();
    ws.closeAll(0);
    expect(names(ws)).toEqual(['pane:ses_1']);
    // shown again as a tab (the agent menu's Show): it keeps the lock and a pin
    ws.setPinned(0, id);
    ws.placePane('ses_1', 'tab');
    expect(ws.paneTabAt('ses_1')!.tab).toMatchObject({ pinned: true, locked: true });
    ws.setLocked(0, id, false);
    ws.closeTab(0, id);
    expect(ws.closingPane).toBe('ses_1');
  });

  it('stay when their file is trashed, showing it as deleted', async () => {
    const { ws } = card('a.ts', 'l.ts');
    await settle();
    ws.setLocked(0, tab(ws, 'l.ts').id);
    await ws.trash(fileKey('local', `${ROOT}/l.ts`));
    await ws.trash(fileKey('local', `${ROOT}/a.ts`));
    expect(names(ws)).toEqual(['l.ts']);
    expect(ws.docs.at(fileKey('local', `${ROOT}/l.ts`))?.deleted).toBe(true);
  });

  it('stay when their part leaves the project, as an outside file', () => {
    // three parts, so that the card stays the Root card of two
    const p = project([
      { id: 'pt_admin', name: 'admin', machineId: 'srv-a', path: '/srv/admin' },
      { id: 'pt_web', name: 'web', machineId: 'local', path: '/srv/web' },
      { id: 'pt_api', name: 'api', machineId: 'local', path: '/srv/api' },
    ]);
    const root = Workspace.get(p);
    const locked = fileKey('srv-a', '/srv/admin/locked.php');
    const clean = fileKey('srv-a', '/srv/admin/clean.php');
    root.open(locked, { pin: true });
    root.open(clean, { pin: true });
    root.setLocked(0, root.groups[0].tabs.find((t) => t.path === locked)!.id);
    expect(Workspace.get({ ...p, parts: p.parts!.filter((x) => x.id !== 'pt_admin') })).toBe(root);
    expect(root.groups[0].tabs.map((t) => t.path)).toEqual([locked]);
  });
});

describe('saved with the tab', () => {
  it('both flags come back after a reload; a state from before them reads as neither', () => {
    const { p, ws } = card('a.ts', 'b.ts', 'c.ts');
    ws.setPinned(0, tab(ws, 'c.ts').id);
    ws.setLocked(0, tab(ws, 'c.ts').id);
    ws.setLocked(0, tab(ws, 'a.ts').id);
    ws.placePane('ses_1', 'tab');
    ws.setLocked(0, ws.paneTabAt('ses_1')!.tab.id);
    persist(ws);
    const raw = JSON.parse(saved.get(`wb.${p.id}`)!) as { groups: { tabs: Record<string, unknown>[] }[] };
    expect(raw.groups[0].tabs).toEqual([
      { path: `${ROOT}/c.ts`, preview: false, pinned: true, locked: true },
      // opened from the pinned c.ts: after it
      { path: '', preview: false, pane: 'ses_1', locked: true },
      { path: `${ROOT}/a.ts`, preview: false, locked: true },
      { path: `${ROOT}/b.ts`, preview: false },
    ]);
    expect(ws.release()).toBe(true);
    const again = Workspace.get(p);
    expect(again).not.toBe(ws);
    expect(again.groups[0].tabs.map((t) => [t.pane ?? t.path.slice(ROOT.length + 1), !!t.pinned, !!t.locked])).toEqual([
      ['c.ts', true, true],
      ['ses_1', false, true],
      ['a.ts', false, true],
      ['b.ts', false, false],
    ]);

    // an older state: no flags; one edited by hand with a pinned tab late, and a pinned preview
    const q = onePart();
    saved.set(
      `wb.${q.id}`,
      JSON.stringify({
        v: 2,
        groups: [{ tabs: [{ path: `${ROOT}/x.ts`, preview: false }, { path: `${ROOT}/y.ts`, preview: true, pinned: true }, { path: `${ROOT}/z.ts`, preview: false, locked: 'yes' }], active: null }],
      }),
    );
    const old = Workspace.get(q);
    expect(old.groups[0].tabs.map((t) => [t.path.slice(ROOT.length + 1), t.preview, !!t.pinned, !!t.locked])).toEqual([
      ['y.ts', false, true, false],
      ['x.ts', false, false, false],
      ['z.ts', false, false, false],
    ]);
  });
});
