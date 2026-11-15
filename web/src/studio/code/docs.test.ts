// The tab's document store (docs.ts): one model per file key whatever card shows it, held by
// cards and let go when none does, dirty state shared, saves routed by the part's ref.
// Monaco and the hub are fakes here; URIs are Monaco's own URI class.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// @ts-expect-error: Monaco's internal module has no types of its own here
import { URI } from 'monaco-editor/base/common/uri.js';

type Listener = () => void;

class FakeModel {
  alt = 1;
  disposed = false;
  private subs = new Set<Listener>();
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
  onDidChangeContent(fn: Listener) {
    this.subs.add(fn);
    return { dispose: () => this.subs.delete(fn) };
  }
  pushStackElement() {}
  pushEditOperations(_s: unknown, edits: { text: string }[]) {
    this.setValue(edits[0].text);
  }
  getFullModelRange() {
    return {};
  }
  dispose() {
    this.disposed = true;
    models.delete(this.uri.toString());
  }
}

const models = new Map<string, FakeModel>();
const files = new Map<string, { content: string; mtime: string }>();
const calls = { read: 0, write: [] as { machineId: string; path: string; projectId?: string; content: string }[], created: 0 };
const hubs = new Map<FakeModel, { attach: (m: FakeModel) => void; detach: (m: FakeModel) => void; saved: (m: FakeModel) => void }>();

vi.mock('./monaco', () => ({
  monaco: {
    Uri: URI,
    editor: {
      getModel: (u: URI) => models.get(u.toString()) ?? null,
      createModel: (text: string, _lang: string | undefined, uri: URI) => {
        calls.created++;
        const m = new FakeModel(text, uri);
        models.set(uri.toString(), m);
        return m;
      },
      getEditors: () => [],
    },
  },
  languageFor: () => 'plaintext',
}));
vi.mock('./lsp', () => ({ hubOf: (m: FakeModel) => hubs.get(m) ?? null }));
vi.mock('../../api', () => ({
  api: {
    fsStat: async (machineId: string, path: string) => {
      const f = files.get(`${machineId}:${path}`);
      return f ? { exists: true, size: f.content.length, mtime: f.mtime } : { exists: false, size: 0, mtime: '' };
    },
    fsRead: async (machineId: string, path: string) => {
      calls.read++;
      const f = files.get(`${machineId}:${path}`);
      if (!f) throw new Error('no such file');
      return { content: f.content, size: f.content.length, mtime: f.mtime, binary: false };
    },
  },
  ApiError: class extends Error {
    status = 0;
  },
  errorMessage: (e: unknown) => String(e),
  fsRawUrl: (m: string, p: string) => `/raw/${m}${p}`,
  isCode: () => false,
}));
vi.mock('../api', () => ({
  filesApi: { onSave: async () => ({ runs: [] }) },
  fsApi: {
    write: async (machineId: string, body: { path: string; content: string; projectId?: string }) => {
      calls.write.push({ machineId, ...body });
      const mtime = `w${calls.write.length}`;
      files.set(`${machineId}:${body.path}`, { content: body.content, mtime });
      return { path: body.path, size: body.content.length, mtime };
    },
  },
}));
vi.mock('../drafts', () => ({ drafts: { get: async () => undefined, put: async () => undefined, remove: async () => undefined } }));
vi.mock('../../store', () => ({ store: { getState: () => ({ settings: {} }) } }));
vi.mock('../../components/Toast', () => ({ toast: { error: () => {}, info: () => {}, success: () => {}, notify: () => {} } }));
vi.mock('../../lib/format', () => ({ prefs: { get: () => null, set: () => {} } }));

const { bindParts, DocStore } = await import('./docs');
const { fileKey } = await import('./paths');
type Holder = import('./docs').Holder;
type DocPart = import('./docs').DocPart;

const flush = () => new Promise((r) => setTimeout(r, 0));
const settle = async () => {
  for (let i = 0; i < 6; i++) await flush();
};

function holder(): Holder & { changes: number; edits: string[]; lents: string[]; gones: string[]; renames: [string, string][] } {
  const h = {
    changes: 0,
    edits: [] as string[],
    lents: [] as string[],
    gones: [] as string[],
    renames: [] as [string, string][],
    changed() {
      h.changes++;
    },
    edited(k: string) {
      h.edits.push(k);
    },
    lent(k: string) {
      h.lents.push(k);
    },
    gone(k: string) {
      h.gones.push(k);
    },
    renamed(a: string, b: string) {
      h.renames.push([a, b]);
    },
  };
  return h as never;
}

function part(ref: string, machineId: string, root: string, views: Holder[] = []) {
  const attached: FakeModel[] = [];
  const hub = { attach: (m: FakeModel) => (attached.push(m), hubs.set(m, hub)), detach: (m: FakeModel) => hubs.delete(m), saved: vi.fn() };
  const rootKey = fileKey(machineId, root);
  const p = {
    ref,
    machineId,
    root,
    rootKey,
    mounted: true,
    lsp: hub,
    laravel: null,
    gitSoon: vi.fn(),
    refreshLaravel: vi.fn(),
    log: vi.fn(),
    holders: () => views,
    relOf: (k: string) => (k === rootKey ? '' : k.startsWith(`${rootKey}/`) ? k.slice(rootKey.length + 1) : null),
    attached,
  };
  return p as unknown as DocPart & { attached: FakeModel[]; gitSoon: ReturnType<typeof vi.fn>; lsp: typeof hub };
}

beforeEach(() => {
  vi.stubGlobal('window', globalThis);
  models.clear();
  files.clear();
  hubs.clear();
  calls.read = 0;
  calls.write = [];
  calls.created = 0;
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('DocStore', () => {
  it('gives two cards one doc and one model, and lets it go when the last card does', async () => {
    files.set('local:/home/me/shop/a.ts', { content: 'one', mtime: 'm1' });
    const store = new DocStore();
    const a = holder();
    const b = holder();
    const key = fileKey('local', '/home/me/shop/a.ts');
    const d1 = store.acquire(key, a, null);
    const d2 = store.acquire(key, b, null);
    expect(d1).toBe(d2);
    await settle();
    expect(calls.created).toBe(1);
    expect(d1.model?.uri.toString()).toBe('file:///home/me/shop/a.ts');
    expect([...store.holders(key)]).toEqual([a, b]);
    store.release(key, a);
    const model = d1.model as unknown as FakeModel;
    expect(model.disposed).toBe(false);
    expect(store.get(key)).toBe(d1);
    store.release(key, b);
    expect(model.disposed).toBe(true);
    expect(store.get(key)).toBeUndefined();
  });

  it('keeps the same path on two machines as two files', async () => {
    files.set('local:/srv/app/x.ts', { content: 'here', mtime: 'm1' });
    files.set('srv-a:/srv/app/x.ts', { content: 'there', mtime: 'm1' });
    const store = new DocStore();
    const h = holder();
    const here = store.acquire(fileKey('local', '/srv/app/x.ts'), h, null);
    const there = store.acquire(fileKey('srv-a', '/srv/app/x.ts'), h, null);
    await settle();
    expect(here).not.toBe(there);
    expect(here.model?.getValue()).toBe('here');
    expect(there.model?.getValue()).toBe('there');
    expect(here.model?.uri.toString()).toBe('file:///srv/app/x.ts');
    expect(there.model?.uri.toString()).toBe('file://m-7372762d61/srv/app/x.ts');
  });

  it('shares dirty state: an edit marks both cards, a save from either clears both, routed by the ref', async () => {
    files.set('srv-a:/srv/app/x.ts', { content: 'v1', mtime: 'm1' });
    const pt = part('p_shop~pt_api', 'srv-a', '/srv/app');
    bindParts({ byRef: (ref) => (ref === pt.ref ? pt : null), holding: () => pt });
    const store = new DocStore();
    const a = holder();
    const b = holder();
    const key = fileKey('srv-a', '/srv/app/x.ts');
    const d = store.acquire(key, a, pt);
    store.acquire(key, b, pt);
    await settle();
    expect(pt.attached).toHaveLength(1);
    const before = { a: a.changes, b: b.changes };
    (d.model as unknown as FakeModel).setValue('v2');
    expect(d.dirty).toBe(true);
    expect(a.edits).toEqual([key]);
    expect(b.edits).toEqual([key]);
    expect(a.changes).toBeGreaterThan(before.a);
    expect(b.changes).toBeGreaterThan(before.b);
    const ok = await store.save(key, { ref: pt.ref, run: (fn) => fn({}) });
    expect(ok).toBe(true);
    expect(d.dirty).toBe(false);
    expect(calls.write).toEqual([{ machineId: 'srv-a', path: '/srv/app/x.ts', content: 'v2', projectId: 'p_shop~pt_api', ifMtime: 'm1', allowOutside: undefined }]);
    expect(pt.gitSoon).toHaveBeenCalled();
    expect(pt.lsp.saved).toHaveBeenCalled();
  });

  it('reloads once for a change two cards’ watches both report', async () => {
    files.set('local:/w/a.txt', { content: 'old', mtime: 'm1' });
    const store = new DocStore();
    const key = fileKey('local', '/w/a.txt');
    const d = store.acquire(key, holder(), null);
    await settle();
    const reads = calls.read;
    files.set('local:/w/a.txt', { content: 'new', mtime: 'm2' });
    store.onDiskChange(key, true, 'm2');
    store.onDiskChange(key, true, 'm2');
    await settle();
    expect(calls.read - reads).toBe(1);
    expect(d.model?.getValue()).toBe('new');
    store.onDiskChange(key, true, 'm2');
    await settle();
    expect(calls.read - reads).toBe(1);
  });

  it('lends a model to a language server: the part’s cards see it, and it goes after a while unless a card took it', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    files.set('local:/usr/lib/x.d.ts', { content: 'declare', mtime: 'm1' });
    const view = holder();
    const pt = part('p~a', 'local', '/home/me/a', [view]);
    bindParts({ byRef: () => pt, holding: () => null });
    const store = new DocStore();
    const key = fileKey('local', '/usr/lib/x.d.ts');
    const p = store.ensureModel(key, pt);
    await vi.advanceTimersByTimeAsync(0);
    const model = (await p) as unknown as FakeModel;
    expect(model.getValue()).toBe('declare');
    expect(view.lents).toEqual([key]);
    expect(pt.attached).toEqual([model]);
    await vi.advanceTimersByTimeAsync(3 * 60_000 + 1);
    expect(model.disposed).toBe(true);
    expect(view.gones).toEqual([key]);

    // taken up by a card meanwhile: it stays
    const q = store.ensureModel(key, pt);
    await vi.advanceTimersByTimeAsync(0);
    const again = (await q) as unknown as FakeModel;
    store.acquire(key, view, pt);
    await vi.advanceTimersByTimeAsync(4 * 60_000);
    expect(again.disposed).toBe(false);
  });

  it('moves the docs under a renamed folder, with unsaved text, for every card that holds them', async () => {
    files.set('local:/w/src/a.ts', { content: 'a', mtime: 'm1' });
    files.set('local:/w/lib/a.ts', { content: 'a', mtime: 'm1' });
    const store = new DocStore();
    const one = holder();
    const two = holder();
    const from = fileKey('local', '/w/src/a.ts');
    const d = store.acquire(from, one, null);
    store.acquire(from, two, null);
    await settle();
    (d.model as unknown as FakeModel).setValue('edited');
    await store.remap(fileKey('local', '/w/src'), fileKey('local', '/w/lib'));
    const to = fileKey('local', '/w/lib/a.ts');
    expect(one.renames).toEqual([[from, to]]);
    expect(two.renames).toEqual([[from, to]]);
    expect(store.get(from)).toBeUndefined();
    expect(store.get(to)?.model?.getValue()).toBe('edited');
    expect([...store.holders(to)]).toEqual([one, two]);
  });
});

describe('Monaco URIs of keys', () => {
  it('round-trips a key through Monaco’s URI string, on This PC and elsewhere', async () => {
    const { keyOfUri, uriPartsOf } = await import('./paths');
    for (const k of [fileKey('local', '/home/me/a b/#x.ts'), fileKey('Srv_A', '/srv/app/ü.ts'), fileKey('mesin-ü', '/'), fileKey('srv-a', '/srv/a%20b')]) {
      const s = URI.from(uriPartsOf(k)).toString();
      expect(keyOfUri(URI.parse(s))).toBe(k);
    }
    expect(URI.from(uriPartsOf(fileKey('local', '/x/y.ts'))).toString()).toBe(URI.file('/x/y.ts').toString());
  });
});
