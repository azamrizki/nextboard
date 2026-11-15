import { describe, expect, it } from 'vitest';
import { ROOT } from '../../lib/parts';
import { byPart, crumbsOf, defaultPlace, fileLabel, fitsCard, isPartRoot, machinesOf, offered, placeChoices, placeDir, tabDir, taskFile, taskKey } from './partGroups';
import { fileKey, ROOT_KEY, ViewPaths, type FileKey, type Mount } from './paths';

const mount = (partId: string, name: string, machineId: string, root: string): Mount => ({ partId, name, machineId, root, rootKey: fileKey(machineId, root), ref: `p_shop~${partId}` });

const admin = mount('pt_admin', 'admin', 'srv-a', '/srv/shop/admin');
const front = mount('pt_front', 'frontend', 'local', '/home/me/shop-front');
const api = mount('pt_api', 'api', 'srv-a', '/srv/shop/api');
const rootView = new ViewPaths([admin, front, api], true);
const adminCard = new ViewPaths([admin], false);
const k = (m: Mount, rel: string) => fileKey(m.machineId, `${m.root}/${rel}`);

describe('byPart', () => {
  it('groups by the part holding each key, in part order, outside last', () => {
    const items: (FileKey | null)[] = [k(api, 'x.go'), k(front, 'a.ts'), fileKey('srv-a', '/usr/lib/z.h'), k(admin, 'b.php'), k(front, 'c.ts'), null];
    const out = byPart(rootView, items, (x) => x);
    expect(out.map((s) => s.mount?.name ?? 'outside')).toEqual(['admin', 'frontend', 'api', 'outside']);
    expect(out[1].items).toEqual([k(front, 'a.ts'), k(front, 'c.ts')]);
    expect(out[3].items).toHaveLength(2);
  });

  it('tells two machines with the same path apart', () => {
    const twin = mount('pt_twin', 'twin', 'srv-b', '/srv/shop/admin');
    const v = new ViewPaths([admin, twin], true);
    const out = byPart(v, [fileKey('srv-b', '/srv/shop/admin/x'), fileKey('srv-a', '/srv/shop/admin/x')], (x) => x);
    expect(out.map((s) => s.mount?.partId)).toEqual(['pt_admin', 'pt_twin']);
  });

  it('a card of one part: one section, the rest outside', () => {
    const out = byPart(adminCard, [k(admin, 'x'), k(front, 'y')], (x) => x);
    expect(out.map((s) => s.mount?.name ?? null)).toEqual(['admin', null]);
  });
});

describe('part folders and machines', () => {
  it('knows a part folder and the virtual root', () => {
    expect(isPartRoot(rootView, admin.rootKey)).toBe(true);
    expect(isPartRoot(rootView, ROOT_KEY)).toBe(true);
    expect(isPartRoot(rootView, k(admin, 'src'))).toBe(false);
    expect(isPartRoot(adminCard, admin.rootKey)).toBe(true);
  });

  it('lists each machine once with its parts, in part order', () => {
    expect(machinesOf([admin, front, api]).map((m) => [m.machineId, m.parts.map((p) => p.name)])).toEqual([
      ['srv-a', ['admin', 'api']],
      ['local', ['frontend']],
    ]);
    expect(machinesOf([mount('x', 'x', '', '/x')])[0].machineId).toBe('local');
  });
});

describe('placement of a terminal or agent', () => {
  it('offers Root and every part in a Root card, the part alone in a part card', () => {
    expect(placeChoices(rootView)).toEqual([ROOT, { partId: 'pt_admin' }, { partId: 'pt_front' }, { partId: 'pt_api' }]);
    expect(placeChoices(adminCard)).toEqual([{ partId: 'pt_admin' }]);
    expect(offered(adminCard, ROOT)).toBe(false);
    expect(offered(rootView, { partId: 'pt_gone' })).toBe(false);
    expect(offered(rootView, null)).toBe(false);
  });

  it('defaults to the active file’s part, else the last choice, else Root', () => {
    expect(defaultPlace(rootView, k(api, 'main.go'), ROOT)).toEqual({ partId: 'pt_api' });
    expect(defaultPlace(rootView, null, { partId: 'pt_front' })).toEqual({ partId: 'pt_front' });
    expect(defaultPlace(rootView, ROOT_KEY, { partId: 'pt_gone' })).toEqual(ROOT);
    expect(defaultPlace(rootView, fileKey('local', '/tmp/x'), null)).toEqual(ROOT);
    // a part card never asks, and never goes to the root
    expect(defaultPlace(adminCard, null, ROOT)).toEqual({ partId: 'pt_admin' });
  });

  it('starts a part in its folder and the root in none', () => {
    expect(placeDir(rootView, { partId: 'pt_api' })).toBe(api.rootKey);
    expect(placeDir(rootView, ROOT)).toBeUndefined();
  });

  it('shows a part card only its own panel agents', () => {
    expect(fitsCard({ partId: 'pt_api' }, rootView, true)).toBe(true);
    expect(fitsCard({ root: true }, rootView, true)).toBe(true);
    expect(fitsCard({ partId: 'pt_admin' }, adminCard, true)).toBe(true);
    expect(fitsCard({ partId: 'pt_api' }, adminCard, true)).toBe(false);
    expect(fitsCard({ root: true }, adminCard, true)).toBe(false);
    expect(fitsCard({ partId: '' }, adminCard, true)).toBe(false);
    // a single-part project: every one, as before
    expect(fitsCard({ partId: '' }, adminCard, false)).toBe(true);
  });
});

describe('labels', () => {
  it('lists files with the part name in a Root card only, and reads them back', () => {
    expect(fileLabel(true, 'admin', 'src/x.php')).toBe('admin/src/x.php');
    expect(fileLabel(false, 'admin', 'src/x.php')).toBe('src/x.php');
    expect(rootView.resolve(fileLabel(true, 'api', 'cmd/main.go'))).toBe(k(api, 'cmd/main.go'));
    expect(adminCard.resolve(fileLabel(false, 'admin', 'src/x.php'))).toBe(k(admin, 'src/x.php'));
  });

  it('starts the crumbs with the part in a Root card', () => {
    expect(crumbsOf(rootView, k(admin, 'src/x.php'))).toEqual([
      { label: 'admin', key: admin.rootKey },
      { label: 'src', key: k(admin, 'src') },
      { label: 'x.php', key: k(admin, 'src/x.php') },
    ]);
    expect(crumbsOf(adminCard, k(admin, 'src/x.php')).map((c) => c.label)).toEqual(['src', 'x.php']);
  });

  it('gives a file outside every part its absolute path, on its machine', () => {
    const out = crumbsOf(rootView, fileKey('srv-a', '/usr/lib/x.h'));
    expect(out.map((c) => c.label)).toEqual(['usr', 'lib', 'x.h']);
    expect(out[1].key).toBe(fileKey('srv-a', '/usr/lib'));
  });

  it('names the part on a tab in a Root card, and the folder of a twin', () => {
    expect(tabDir(rootView, k(admin, 'src/x.php'), false)).toBe('admin');
    expect(tabDir(rootView, k(admin, 'src/x.php'), true)).toBe('admin › src');
    expect(tabDir(rootView, k(admin, 'x.php'), true)).toBe('admin');
    expect(tabDir(adminCard, k(admin, 'src/x.php'), false)).toBe('');
    expect(tabDir(adminCard, k(admin, 'src/x.php'), true)).toBe('src');
    expect(tabDir(rootView, fileKey('srv-a', '/usr/lib/x.h'), true)).toBe('lib');
  });
});

describe('tasks', () => {
  it('keeps the same task of two parts apart', () => {
    expect(taskKey('pt_admin', 'npm:build')).not.toBe(taskKey('pt_api', 'npm:build'));
  });

  it('finds a reported file against the task’s folder, on the part’s machine', () => {
    expect(taskFile('srv-a', '/srv/shop/api', 'cmd', 'main.go')).toBe(fileKey('srv-a', '/srv/shop/api/cmd/main.go'));
    expect(taskFile('srv-a', '/srv/shop/api', '', './x/y.go')).toBe(fileKey('srv-a', '/srv/shop/api/x/y.go'));
    expect(taskFile('local', '/w', 'web', '../lib/a.ts')).toBe('/w/lib/a.ts');
    expect(taskFile('srv-a', '/srv/shop/api', 'cmd', '/abs/z.go')).toBe(fileKey('srv-a', '/abs/z.go'));
  });
});
