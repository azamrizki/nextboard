import { describe, expect, it } from 'vitest';
import { fileKey, PERSIST_VERSION, type FileKey } from './paths';
import { LAYOUT_VERSION, listUnder, MAX_GROUPS, mergeBookmarks, mergeDebug, mergeLegacy, MAX_MERGED_TABS, MAX_RECENT, readAnyPath, readBookmarks, readDebug, readLayout, readState, stateUnder, toolTabId, type Persisted } from './persist';

const SRV = 'srv-a';
const k = (abs: string, machine = 'local') => fileKey(machine, abs);

/** A state as today's client saves it: no version, absolute paths. */
const v1 = (over: Partial<Persisted> = {}): Partial<Persisted> => ({
  groups: [{ tabs: [{ path: '/srv/shop/a.ts', preview: false }, { path: '', preview: false, pane: 'ses_1' }, { path: '/srv/shop/b.ts', preview: true }], active: '/srv/shop/b.ts' }],
  activeGroup: 0,
  expanded: ['/srv/shop', '/srv/shop/src'],
  recent: ['/srv/shop/b.ts', '/srv/shop/a.ts'],
  md: { '/srv/shop/README.md': 'source' },
  side: 'scm',
  ...over,
});

describe('readState: saved workbench state', () => {
  it('reads a local v1 state as it is (keys are the paths on This PC)', () => {
    const s = readState(v1(), 'local');
    expect(s.v).toBe(PERSIST_VERSION);
    expect(s.groups![0].tabs).toEqual([
      { path: '/srv/shop/a.ts', preview: false },
      { path: '', preview: false, pane: 'ses_1' },
      { path: '/srv/shop/b.ts', preview: true },
    ]);
    expect(s.groups![0].active).toBe('/srv/shop/b.ts');
    expect(s.expanded).toEqual(['/srv/shop', '/srv/shop/src']);
    expect(s.recent).toEqual(['/srv/shop/b.ts', '/srv/shop/a.ts']);
    expect(s.md).toEqual({ '/srv/shop/README.md': 'source' });
    expect(s.side).toBe('scm');
  });

  it('puts the paths of a server part’s v1 state on that machine', () => {
    const s = readState(v1(), SRV);
    expect(s.groups![0].tabs[0].path).toBe(k('/srv/shop/a.ts', SRV));
    expect(s.groups![0].tabs[0].path.startsWith('//m-')).toBe(true);
    expect(s.groups![0].tabs[1]).toEqual({ path: '', preview: false, pane: 'ses_1' });
    expect(s.groups![0].active).toBe(k('/srv/shop/b.ts', SRV));
    expect(s.expanded).toEqual([k('/srv/shop', SRV), k('/srv/shop/src', SRV)]);
    expect(Object.keys(s.md!)).toEqual([k('/srv/shop/README.md', SRV)]);
  });

  it('keeps a v2 state’s keys, whatever machine the reader assumes', () => {
    const remote = k('/srv/shop/a.ts', SRV);
    const s = readState({ v: PERSIST_VERSION, groups: [{ tabs: [{ path: remote, preview: false }, { path: '/home/me/x.ts', preview: false }], active: remote }], expanded: [remote] }, 'other');
    expect(s.groups![0].tabs.map((t) => t.path)).toEqual([remote, '/home/me/x.ts']);
    expect(s.groups![0].active).toBe(remote);
  });

  it('drops what names no file, and diff tabs as the active one', () => {
    const s = readState({ groups: [{ tabs: [{ path: 'relative.ts', preview: false }, { path: '/ok.ts', preview: false }], active: 'diff:head:/ok.ts' }], expanded: ['rel', '/a/../b'] }, 'local');
    expect(s.groups![0].tabs).toEqual([{ path: '/ok.ts', preview: false }]);
    expect(s.groups![0].active).toBeNull();
    expect(s.expanded).toEqual(['/b']);
  });

  it('reads nothing as an empty state', () => {
    const s = readState(null, 'local');
    expect(s.groups).toEqual([]);
    expect(s.expanded).toEqual([]);
  });
});

describe('stateUnder: does an old project-level state belong to a part', () => {
  it('needs every tab and folder in the part’s folder on its machine', () => {
    expect(stateUnder(readState(v1(), 'local'), k('/srv/shop'))).toBe(true);
    expect(stateUnder(readState(v1(), 'local'), k('/srv/sho'))).toBe(false);
    expect(stateUnder(readState(v1(), 'local'), k('/srv/shop', SRV))).toBe(false);
    expect(stateUnder(readState(v1(), SRV), k('/srv/shop', SRV))).toBe(true);
    expect(stateUnder(readState(v1({ expanded: ['/srv/shop', '/srv/other'] }), 'local'), k('/srv/shop'))).toBe(false);
  });
  it('an empty state belongs to no part', () => {
    expect(stateUnder(readState({}, 'local'), k('/'))).toBe(false);
  });
});

describe('mergeLegacy: the first Root open', () => {
  const admin = k('/srv/admin', SRV);
  const web = k('/home/me/web');
  const a = readState(
    {
      groups: [
        { tabs: [{ path: '/srv/admin/x.php', preview: false }], active: '/srv/admin/x.php' },
        { tabs: [{ path: '/srv/admin/y.php', preview: false }], active: '/srv/admin/y.php' },
      ],
      activeGroup: 1,
      expanded: ['/srv/admin', '/srv/admin/app'],
      recent: ['/srv/admin/y.php', '/srv/admin/x.php'],
      side: 'search',
    },
    SRV,
  );
  const b = readState(
    {
      groups: [{ tabs: [{ path: '/home/me/web/index.ts', preview: true }, { path: '', preview: false, pane: 'ses_9' }], active: '/home/me/web/index.ts' }],
      expanded: ['/home/me/web'],
      recent: ['/home/me/web/index.ts', '/srv/admin/x.php'],
      md: { '/home/me/web/README.md': 'split' },
    },
    'local',
  );

  it('takes the layout from the first state with tabs and pins the others’ tabs into group 1', () => {
    const m = mergeLegacy([a, b], [admin, web]);
    expect(m.v).toBe(PERSIST_VERSION);
    expect(m.side).toBe('search');
    expect(m.activeGroup).toBe(1);
    expect(m.groups!.map((g) => g.tabs.map((t) => t.pane ?? t.path))).toEqual([
      [k('/srv/admin/x.php', SRV), k('/home/me/web/index.ts'), 'ses_9'],
      [k('/srv/admin/y.php', SRV)],
    ]);
    expect(m.groups![0].tabs[1].preview).toBe(false);
  });

  it('combines expanded folders with the part roots, recent files in order, Markdown views', () => {
    const m = mergeLegacy([a, b], [admin, web]);
    expect(m.expanded).toEqual([admin, k('/srv/admin/app', SRV), web]);
    expect(m.recent).toEqual([k('/srv/admin/y.php', SRV), k('/srv/admin/x.php', SRV), k('/home/me/web/index.ts'), k('/srv/admin/x.php')]);
    expect(m.md).toEqual({ [k('/home/me/web/README.md')]: 'split' });
  });

  it('a state without tabs gives its folders only; nothing at all gives the part roots', () => {
    const empty = readState({ expanded: ['/home/me/web/src'] }, 'local');
    const m = mergeLegacy([empty, a], [admin, web]);
    expect(m.groups![0].tabs[0].path).toBe(k('/srv/admin/x.php', SRV));
    expect(m.expanded).toContain(k('/home/me/web/src'));
    const none = mergeLegacy([], [admin, web]);
    expect(none.groups).toEqual([{ tabs: [], active: null }]);
    expect(none.expanded).toEqual([admin, web]);
  });

  it('deduplicates and caps the tabs it takes over, and caps recent files', () => {
    const many = readState({ groups: [{ tabs: Array.from({ length: 60 }, (_, i) => ({ path: `/home/me/web/f${i}.ts`, preview: false })), active: null }], recent: Array.from({ length: 80 }, (_, i) => `/home/me/web/r${i}.ts`) }, 'local');
    const m = mergeLegacy([a, many, many], [admin, web]);
    const all = m.groups!.flatMap((g) => g.tabs);
    expect(all.length).toBe(MAX_MERGED_TABS);
    expect(new Set(all.map((t) => t.path)).size).toBe(all.length);
    expect(m.recent!.length).toBe(MAX_RECENT);
  });
});

describe('bookmarks and breakpoints', () => {
  it('reads plain paths on the part’s machine and keys as keys', () => {
    expect(readAnyPath('/srv/x.ts', SRV)).toBe(k('/srv/x.ts', SRV));
    expect(readAnyPath(k('/srv/x.ts', SRV), 'local')).toBe(k('/srv/x.ts', SRV));
    expect(readAnyPath('/home/x.ts', 'local')).toBe('/home/x.ts');
    expect(readAnyPath('rel.ts', 'local')).toBeNull();
    expect(readBookmarks([{ path: '/srv/x.ts', line: 3 }, { path: 'bad', line: 1 }, { nope: 1 }], SRV)).toEqual([{ path: k('/srv/x.ts', SRV), line: 3 }]);
    expect(readBookmarks('junk', 'local')).toEqual([]);
  });

  it('reads a v1 debug state on the machine, a v2 one as is', () => {
    const d1 = readDebug({ breakpoints: [{ id: 1, path: '/srv/x.go', line: 4, enabled: true }], watches: ['a'], config: 'Debug', filters: [] }, SRV);
    expect(d1.breakpoints[0].path).toBe(k('/srv/x.go', SRV));
    expect(d1.v).toBe(PERSIST_VERSION);
    const d2 = readDebug({ v: PERSIST_VERSION, breakpoints: [{ id: 1, path: '/home/x.go', line: 4, enabled: true }], watches: [], config: '', filters: [] }, SRV);
    expect(d2.breakpoints[0].path).toBe('/home/x.go');
    expect(readDebug(null, 'local')).toEqual({ v: PERSIST_VERSION, breakpoints: [], watches: [], config: '', filters: [], part: undefined });
  });

  it('adopts a list only when all of it lies in the part', () => {
    const root = k('/srv/app', SRV) as FileKey;
    expect(listUnder([{ path: k('/srv/app/a', SRV) }], root)).toBe(true);
    expect(listUnder([{ path: k('/srv/app2/a', SRV) }], root)).toBe(false);
    expect(listUnder([], root)).toBe(false);
  });

  it('merges bookmarks and breakpoints without twins, with fresh breakpoint ids', () => {
    expect(mergeBookmarks([[{ path: '/a', line: 1 }], [{ path: '/a', line: 1 }, { path: '/a', line: 2 }]])).toEqual([
      { path: '/a', line: 1 },
      { path: '/a', line: 2 },
    ]);
    const m = mergeDebug([
      { breakpoints: [{ id: 7, path: '/a', line: 1, enabled: true }], watches: ['x'], config: '', filters: ['uncaught'] },
      { breakpoints: [{ id: 7, path: '/b', line: 1, enabled: true }, { id: 9, path: '/a', line: 1, enabled: false }], watches: ['x', 'y'], config: 'Go', filters: [] },
    ]);
    expect(m.breakpoints.map((b) => [b.id, b.path])).toEqual([
      [1, '/a'],
      [2, '/b'],
    ]);
    expect(m.watches).toEqual(['x', 'y']);
    expect(m.config).toBe('Go');
    expect(m.filters).toEqual(['uncaught']);
  });
});

describe('tool tabs (the code view)', () => {
  it('are kept with their kind and argument; an unknown kind is dropped', () => {
    const s = readState(
      {
        v: PERSIST_VERSION,
        groups: [
          {
            tabs: [
              { path: '', preview: false, tool: { kind: 'vault' }, pinned: true },
              { path: '', preview: true, tool: { kind: 'browser', arg: 'http://localhost:5173/cart' } },
              { path: '', preview: false, tool: { kind: 'teleporter' } } as never,
              { path: '/srv/shop/a.ts', preview: false },
            ],
            active: toolTabId('browser', 'http://localhost:5173/cart'),
          },
        ],
      },
      'local',
    );
    expect(s.groups![0].tabs).toEqual([
      { path: '', preview: false, tool: { kind: 'vault' }, pinned: true },
      { path: '', preview: false, tool: { kind: 'browser', arg: 'http://localhost:5173/cart' } },
      { path: '/srv/shop/a.ts', preview: false },
    ]);
    expect(s.groups![0].active).toBe('tool:browser:http://localhost:5173/cart');
  });

  it('name no file: a state of tools only belongs to no part', () => {
    const s = readState({ v: PERSIST_VERSION, groups: [{ tabs: [{ path: '', preview: false, tool: { kind: 'chats' } }], active: null }] }, 'local');
    expect(stateUnder(s, k('/srv/shop'))).toBe(false);
  });

  it('one tab per tool kind, a browser one per address', () => {
    expect(toolTabId('memory')).toBe('tool:memory');
    expect(toolTabId('memory', 'ignored')).toBe('tool:memory');
    expect(toolTabId('browser')).toBe('tool:browser:');
  });
});

describe('readLayout: the groups and where they lie', () => {
  const g = (name: string) => ({ tabs: [{ path: `/srv/shop/${name}`, preview: false }], active: `/srv/shop/${name}` });
  const empty = { tabs: [], active: null };

  it('migrates the row of before (no layout): two side by side, a third under the second', () => {
    expect(MAX_GROUPS).toBe(4);
    const l = readLayout({ groups: [g('a.ts'), g('b.ts'), g('c.ts')], activeGroup: 2 })!;
    expect(l.groups.map((x) => x.active)).toEqual(['/srv/shop/a.ts', '/srv/shop/b.ts', '/srv/shop/c.ts']);
    expect(l.grid).toEqual({ dir: 'row', a: { g: 0 }, b: { dir: 'col', a: { g: 1 }, b: { g: 2 }, at: 0.5 }, at: 0.5 });
    expect(l.activeGroup).toBe(2);
  });

  it('reads a saved grid, in its reading order, with its sizes', () => {
    const grid = { v: LAYOUT_VERSION, root: { dir: 'col', a: { dir: 'row', a: { g: 2 }, b: { g: 0 }, at: 0.7 }, b: { g: 1 }, at: 0.4 } } as Persisted['grid'];
    const l = readLayout({ groups: [g('a.ts'), g('b.ts'), g('c.ts')], grid, activeGroup: 0 })!;
    // reading order: c (top left), a (top right), b (bottom)
    expect(l.groups.map((x) => x.active)).toEqual(['/srv/shop/c.ts', '/srv/shop/a.ts', '/srv/shop/b.ts']);
    expect(l.grid).toEqual({ dir: 'col', a: { dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.7 }, b: { g: 2 }, at: 0.4 });
    // the active group follows its group, not its index
    expect(l.activeGroup).toBe(1);
  });

  it('drops a group with no tab left, and its place goes to its neighbour', () => {
    const grid = { v: LAYOUT_VERSION, root: { dir: 'row', a: { g: 0 }, b: { dir: 'col', a: { g: 1 }, b: { g: 2 }, at: 0.5 }, at: 0.3 } } as Persisted['grid'];
    const l = readLayout({ groups: [g('a.ts'), empty, g('c.ts')], grid, activeGroup: 1 })!;
    expect(l.groups.map((x) => x.active)).toEqual(['/srv/shop/a.ts', '/srv/shop/c.ts']);
    expect(l.grid).toEqual({ dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.3 });
    expect(l.activeGroup).toBe(0);
  });

  it('reads a layout that does not match its groups, or of another version, as the row', () => {
    const bad = { v: LAYOUT_VERSION, root: { dir: 'row', a: { g: 0 }, b: { g: 0 }, at: 0.5 } } as Persisted['grid'];
    expect(readLayout({ groups: [g('a.ts'), g('b.ts')], grid: bad })!.grid).toEqual({ dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.5 });
    const newer = { v: LAYOUT_VERSION + 1, root: { dir: 'col', a: { g: 0 }, b: { g: 1 }, at: 0.5 } } as Persisted['grid'];
    expect(readLayout({ groups: [g('a.ts'), g('b.ts')], grid: newer })!.grid).toEqual({ dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.5 });
  });

  it('is nothing without a tab', () => {
    expect(readLayout({ groups: [empty] })).toBeNull();
    expect(readLayout({})).toBeNull();
  });

  it('a merged first Root open drops the layout its groups no longer match', () => {
    const grid = { v: LAYOUT_VERSION, root: { dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.5 } } as Persisted['grid'];
    const m = mergeLegacy([readState({ v: PERSIST_VERSION, groups: [empty, g('a.ts')], grid } as Partial<Persisted>, 'local')], []);
    expect(m.grid).toBeUndefined();
    expect(readLayout(m)!.grid).toEqual({ g: 0 });
  });
});
