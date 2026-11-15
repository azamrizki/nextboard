// An editor card's view (workspace.ts) across parts: view keys, saved state from before parts
// were equal, the Root card's tree, one shared doc for two cards, where a terminal starts.
// The hub, Monaco and the language servers are fakes.

import { beforeEach, describe, expect, it, vi } from 'vitest';
// @ts-expect-error: Monaco's internal module has no types of its own here
import { URI } from 'monaco-editor/base/common/uri.js';

const saved = new Map<string, string>();
const files = new Map<string, { content: string; mtime: string }>();
const trees = new Map<string, { name: string; dir: boolean }[]>();

class FakeModel {
  alt = 1;
  disposed = false;
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
  getAlternativeVersionId() {
    return this.alt;
  }
  onDidChangeContent(fn: () => void) {
    this.subs.add(fn);
    return { dispose: () => this.subs.delete(fn) };
  }
  dispose() {
    this.disposed = true;
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
    search: vi.fn(async (ref: string) => ({ files: [{ rel: `${ref}.txt`, matches: [] }], count: 1, truncated: false, engine: 'rg' })),
  },
  fsApi: {
    tree: async (m: string, p: string) => ({ path: p, entries: (trees.get(`${m}:${p}`) ?? []).map((e) => ({ ...e, size: 0, hidden: false, binary: false })), truncated: false }),
    watch: async () => ({ watchId: 'w1' }),
    unwatch: async () => ({}),
    renewWatch: async () => ({}),
    write: async (_m: string, b: { path: string }) => ({ path: b.path, size: 0, mtime: 'w' }),
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
vi.mock('../../components/Toast', () => ({ toast: { error: () => {}, info: () => {}, success: () => {}, notify: () => {} } }));
vi.mock('../../lib/format', () => ({ prefs: { get: (k: string) => saved.get(k) ?? null, set: (k: string, v: string) => saved.set(k, v) } }));
vi.mock('../../board/files', () => ({ sendPasted: async () => [] }));
vi.mock('../../lib/pack', () => ({ packUpload: async () => ({}) }));

vi.stubGlobal('window', Object.assign(globalThis, { localStorage: { removeItem: (k: string) => saved.delete(k.replace(/^nb\./, '')) } }));

const { Workspace } = await import('./workspace');
const { fileKey, ROOT_KEY } = await import('./paths');
const { filesApi } = await import('../api');
type Project = import('../../lib/types').Project;

const settle = async () => {
  for (let i = 0; i < 8; i++) await new Promise((r) => setTimeout(r, 0));
};

let seq = 0;
const project = (parts: NonNullable<Project['parts']>): Project => ({
  id: `p_shop${++seq}`,
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

beforeEach(() => {
  saved.clear();
  files.clear();
  trees.clear();
});

describe('a single-part project behaves as before', () => {
  it('on This PC: the view key is the project id and a saved state comes back with the same paths', async () => {
    const p = project([{ id: 'pt_main', name: 'Main', machineId: 'local', path: '/home/me/shop/' }]);
    files.set('local:/home/me/shop/a.ts', { content: 'a', mtime: 'm1' });
    saved.set(`wb.${p.id}`, JSON.stringify({ groups: [{ tabs: [{ path: '/home/me/shop/a.ts', preview: false }], active: '/home/me/shop/a.ts' }], expanded: ['/home/me/shop'] }));
    const ws = Workspace.get(p);
    expect(ws.viewKey).toBe(p.id);
    expect(ws.pid).toBe(p.id);
    expect(ws.rootKey).toBe('/home/me/shop');
    expect(ws.activeTab?.path).toBe('/home/me/shop/a.ts');
    await settle();
    expect(ws.activeDoc?.model?.uri.toString()).toBe('file:///home/me/shop/a.ts');
    expect(ws.toKey('/home/me/shop/a.ts')).toBe('/home/me/shop/a.ts');
    expect(ws.rel('/home/me/shop/a.ts')).toBe('a.ts');
    expect(ws.rel('/home/me/shop')).toBe('/home/me/shop');
    expect(ws.inside('/usr/lib/x')).toBe(false);
    expect(Workspace.get(p)).toBe(ws);
  });

  it('on a server: old saved paths land on its machine, plain paths are read on it', () => {
    const p = project([{ id: 'pt_main', name: 'Main', machineId: 'srv-a', path: '/srv/shop' }]);
    saved.set(`wb.${p.id}`, JSON.stringify({ groups: [{ tabs: [{ path: '/srv/shop/src/x.ts', preview: false }], active: '/srv/shop/src/x.ts' }] }));
    const ws = Workspace.get(p);
    const key = fileKey('srv-a', '/srv/shop/src/x.ts');
    expect(ws.activeTab?.path).toBe(key);
    expect(ws.toKey('/srv/shop/src/x.ts')).toBe(key);
    expect(ws.rel(key)).toBe('src/x.ts');
    expect(ws.abs('src/x.ts')).toBe(key);
    expect(ws.mid).toBe('srv-a');
    expect(ws.placeOf()).toEqual({ partId: 'pt_main', machineId: 'srv-a', cwd: '/srv/shop' });
  });
});

describe('a project with two parts', () => {
  const twoParts = () =>
    project([
      { id: 'pt_admin', name: 'admin', machineId: 'srv-a', path: '/srv/app' },
      { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/srv/app' },
    ]);

  it('a card in a part takes over the old project-level state only when it lies in that part', () => {
    const p = twoParts();
    saved.set(`wb.${p.id}`, JSON.stringify({ groups: [{ tabs: [{ path: '/srv/app/admin.php', preview: false }], active: '/srv/app/admin.php' }] }));
    const admin = Workspace.get(p, { partId: 'pt_admin' });
    expect(admin.viewKey).toBe(`${p.id}~pt_admin`);
    expect(admin.activeTab?.path).toBe(fileKey('srv-a', '/srv/app/admin.php'));
    const web = Workspace.get(p, { partId: 'pt_web' });
    // the same folder on This PC: the old state is read on its part's machine, so it fits here too
    expect(web.activeTab?.path).toBe('/srv/app/admin.php');
    const q = twoParts();
    saved.set(`wb.${q.id}`, JSON.stringify({ groups: [{ tabs: [{ path: '/elsewhere/x', preview: false }], active: null }] }));
    expect(Workspace.get(q, { partId: 'pt_web' }).groups[0].tabs).toEqual([]);
  });

  it('the Root card shows each part as a top folder and merges the old states once', async () => {
    const p = twoParts();
    saved.set(`wb.${p.id}~pt_web`, JSON.stringify({ groups: [{ tabs: [{ path: '/srv/app/index.ts', preview: false }], active: '/srv/app/index.ts' }] }));
    saved.set(`wb.part.${p.id}`, 'pt_web');
    const root = Workspace.get(p);
    expect(root.viewKey).toBe(`${p.id}@root`);
    expect(root.rootKey).toBe(ROOT_KEY);
    expect(saved.has(`wb.part.${p.id}`)).toBe(false);
    expect(root.groups[0].tabs.map((t) => t.path)).toEqual(['/srv/app/index.ts']);
    expect([...root.expanded]).toEqual(expect.arrayContaining([ROOT_KEY, fileKey('srv-a', '/srv/app'), '/srv/app']));
    await root.loadDir(ROOT_KEY);
    const top = root.items.get(ROOT_KEY) as { name: string; key?: string }[];
    expect(top.map((e) => [e.name, e.key])).toEqual([
      ['admin', fileKey('srv-a', '/srv/app')],
      ['frontend', '/srv/app'],
    ]);
    expect(root.label(fileKey('srv-a', '/srv/app/x.php'))).toBe('/admin/x.php');
    expect(root.label('/srv/app/x.ts' as never)).toBe('/frontend/x.ts');
    expect(root.placeOf()).toEqual({ root: true });
    expect(root.placeOf(fileKey('srv-a', '/srv/app/sub'))).toEqual({ partId: 'pt_admin', machineId: 'srv-a', cwd: '/srv/app/sub' });
  });

  it('two cards on one file share its doc: the Root card and a part card', async () => {
    const p = twoParts();
    files.set('srv-a:/srv/app/x.php', { content: '<?php', mtime: 'm1' });
    const root = Workspace.get(p);
    const admin = Workspace.get(p, { partId: 'pt_admin' });
    const key = fileKey('srv-a', '/srv/app/x.php');
    root.open(key, { pin: true });
    admin.open(key, { pin: true });
    await settle();
    expect(root.docs.at(key)).toBe(admin.docs.at(key));
    (root.docs.at(key)!.model as unknown as FakeModel).setValue('<?php echo 1;');
    expect(admin.docs.at(key)?.dirty).toBe(true);
    admin.closeTab(0, admin.groups[0].tabs[0].id);
    expect(admin.closing).toBeNull(); // the Root card still shows the unsaved edits
    expect(root.docs.at(key)?.model?.getValue()).toBe('<?php echo 1;');
    expect(admin.readOnlyOf(root.docs.at(key)!)).toBe('');
    expect(root.partOf(key)?.ref).toBe(`${p.id}~pt_admin`);
  });

  it('searches every part, sending a part-named glob only to that part', async () => {
    const p = twoParts();
    const root = Workspace.get(p);
    const search = vi.mocked(filesApi.search);
    search.mockClear();
    const all = await root.searchParts({ query: 'x' });
    expect(search.mock.calls.map((c) => c[0])).toEqual([`${p.id}~pt_admin`, `${p.id}~pt_web`]);
    expect(all.count).toBe(2);
    expect(all.parts[0].files[0].key).toBe(fileKey('srv-a', `/srv/app/${p.id}~pt_admin.txt`));
    search.mockClear();
    await root.searchParts({ query: 'x', include: 'admin/src/**' });
    expect(search.mock.calls.map((c) => [c[0], c[1].include])).toEqual([[`${p.id}~pt_admin`, 'src/**']]);
  });
});
