// One LspHub per part (lsp.ts): what a server names is mapped back to the hub's machine, a hub
// takes only its own machine's models from its folder (at a "/" boundary), and a model is served
// by exactly one hub, the one providers then ask.

import { describe, expect, it, vi } from 'vitest';
// @ts-expect-error: Monaco's internal module has no types of its own here
import { URI } from 'monaco-editor/base/common/uri.js';

interface Model {
  uri: URI;
  getLanguageId(): string;
  onDidChangeContent(): { dispose(): void };
}
const models: Model[] = [];
const markers: { uri: string; owner: string; list: { message: string }[] }[] = [];

vi.mock('./monaco', () => ({
  monaco: {
    Uri: URI,
    editor: {
      getModels: () => models,
      getModel: (u: URI) => models.find((m) => m.uri.toString() === u.toString()) ?? null,
      setModelMarkers: (m: Model, owner: string, list: { message: string }[]) => markers.push({ uri: m.uri.toString(), owner, list }),
    },
    MarkerSeverity: { Error: 8, Warning: 4, Info: 2, Hint: 1 },
  },
  disableBuiltinTypescript: () => {},
}));
vi.mock('./nativeExt', () => ({ hasNative: () => false, onNativeChange: () => () => {} }));
vi.mock('../../api', () => ({ wsUrl: (p: string) => p }));
vi.mock('../../components/Toast', () => ({ toast: { error: () => {} } }));
vi.mock('../api', () => ({ filesApi: { lspStatus: async () => ({ items: [] }) } }));

const { LspHub, hubOf } = await import('./lsp');
const { authorityOf, fileKey, uriPartsOf } = await import('./paths');

const model = (machineId: string, abs: string): Model => {
  const m = { uri: URI.from(uriPartsOf(fileKey(machineId, abs))), getLanguageId: () => 'typescript', onDidChangeContent: () => ({ dispose() {} }) };
  models.push(m);
  return m;
};

const hub = (machineId: string, root: string, id = `${machineId}:${root}`) =>
  new LspHub({ id, ref: id, root, machineId, authority: authorityOf(machineId), ensureModel: async () => null, log: () => {}, changed: () => {} });

describe('LspHub across parts', () => {
  it('maps what a server names to the model URI and key on the hub’s machine', () => {
    const h = hub('srv-a', '/srv/app');
    expect(h.modelUri('file:///srv/app/x.ts').toString()).toBe('file://m-7372762d61/srv/app/x.ts');
    expect(h.keyOf('file:///srv/app/a%20b.ts')).toBe(fileKey('srv-a', '/srv/app/a b.ts'));
    expect(hub('local', '/srv/app').modelUri('file:///srv/app/x.ts').toString()).toBe('file:///srv/app/x.ts');
  });

  it('resume() takes its folder’s models on its machine only: not /srv/app2, not the same path elsewhere', () => {
    models.length = 0;
    const inside = model('srv-a', '/srv/app/x.ts');
    const root = model('srv-a', '/srv/app');
    const sibling = model('srv-a', '/srv/app2/x.ts');
    const otherMachine = model('local', '/srv/app/x.ts');
    const h = hub('srv-a', '/srv/app');
    h.resume();
    expect(hubOf(inside as never)).toBe(h);
    expect(hubOf(root as never)).toBe(h);
    expect(hubOf(sibling as never)).toBeNull();
    expect(hubOf(otherMachine as never)).toBeNull();
    h.dispose();
    expect(hubOf(inside as never)).toBeNull();
  });

  it('serves a model from one hub at a time: attaching elsewhere takes it over', () => {
    models.length = 0;
    const m = model('local', '/home/me/web/x.ts');
    const a = hub('local', '/home/me/api');
    const b = hub('local', '/home/me/web');
    a.attach(m as never);
    expect(hubOf(m as never)).toBe(a);
    b.attach(m as never);
    expect(hubOf(m as never)).toBe(b);
    a.detach(m as never);
    expect(hubOf(m as never)).toBe(b);
  });

  it('puts a server’s diagnostics on the model of its own machine, not on the same path elsewhere', () => {
    models.length = 0;
    markers.length = 0;
    const here = model('local', '/srv/app/x.ts');
    const there = model('srv-a', '/srv/app/x.ts');
    const h = hub('srv-a', '/srv/app');
    h.applyDiagnostics('file:///srv/app/x.ts', [{ range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } }, message: 'boom' }]);
    expect(markers.map((x) => x.uri)).toEqual([there.uri.toString()]);
    expect(markers[0].uri).not.toBe(here.uri.toString());
  });
});
