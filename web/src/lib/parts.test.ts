import { describe, expect, it } from 'vitest';
import { fileKey, ROOT_KEY, ViewPaths } from '../studio/code/paths';
import { isRoot, mountsOf, normalPlacement, partHoldingOn, partRef, placementOf, placementOfSession, refOf, ROOT, samePlacement, splitRef } from './parts';
import type { Project } from './types';

const project = (parts: Project['parts']): Project => ({
  id: 'p_shop',
  name: 'Shop',
  machineId: parts?.[0]?.machineId ?? 'local',
  path: parts?.[0]?.path ?? '/home/me/shop',
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});

const two = project([
  { id: 'main', name: 'Main', machineId: 'local', path: '/srv/shop' },
  { id: 'pt_api', name: 'Api', machineId: 'srv-a', path: '/srv/shop' },
]);
const one = project([{ id: 'pt_web', name: 'Web', machineId: 'srv-a', path: '/srv/web/' }]);

describe('part refs', () => {
  it('keys every part "<project>~<part>", the first included', () => {
    expect(partRef(two, two.parts![0])).toBe('p_shop~main');
    expect(partRef(two, two.parts![1])).toBe('p_shop~pt_api');
    expect(refOf('p_shop', 'main')).toBe('p_shop~main');
    expect(splitRef('p_shop~pt_api')).toEqual({ projectId: 'p_shop', partId: 'pt_api' });
    expect(splitRef('p_shop')).toEqual({ projectId: 'p_shop', partId: '' });
  });
});

describe('placement', () => {
  it('reads an editor node: a known part, else the root; a single part is always that part', () => {
    expect(placementOf({ part: 'pt_api' }, two)).toEqual({ partId: 'pt_api' });
    expect(placementOf({}, two)).toEqual(ROOT);
    expect(placementOf({ part: '' }, two)).toEqual(ROOT);
    expect(placementOf({ part: 'pt_gone' }, two)).toEqual(ROOT);
    expect(placementOf({ part: 7 }, two)).toEqual(ROOT);
    expect(placementOf(null, two)).toEqual(ROOT);
    expect(placementOf(undefined, one)).toEqual({ partId: 'pt_web' });
    expect(normalPlacement(one, ROOT)).toEqual({ partId: 'pt_web' });
  });

  it('reads a session: root, its part, or none', () => {
    expect(placementOfSession({ root: true }, two)).toEqual(ROOT);
    expect(placementOfSession({ partId: 'main' }, two)).toEqual({ partId: 'main' });
    expect(placementOfSession({ partId: '' }, two)).toBeNull();
    expect(placementOfSession({}, two)).toBeNull();
    expect(placementOfSession({ partId: 'pt_gone' }, two)).toBeNull();
    expect(placementOfSession({ root: true }, one)).toEqual({ partId: 'pt_web' });
  });

  it('compares placements', () => {
    expect(isRoot(ROOT)).toBe(true);
    expect(isRoot({ partId: 'main' })).toBe(false);
    expect(samePlacement(ROOT, { root: true })).toBe(true);
    expect(samePlacement({ partId: 'a' }, { partId: 'a' })).toBe(true);
    expect(samePlacement({ partId: 'a' }, ROOT)).toBe(false);
    expect(samePlacement(ROOT, { partId: 'a' })).toBe(false);
  });
});

describe('partHoldingOn', () => {
  it('tells two parts with the same path on two machines apart', () => {
    expect(partHoldingOn(two, 'local', '/srv/shop/x')?.id).toBe('main');
    expect(partHoldingOn(two, 'srv-a', '/srv/shop/x')?.id).toBe('pt_api');
    expect(partHoldingOn(two, 'srv-b', '/srv/shop/x')).toBeUndefined();
    expect(partHoldingOn(two, 'local', '/srv/shop2/x')).toBeUndefined();
    expect(partHoldingOn(one, 'srv-a', '/srv/web')?.id).toBe('pt_web');
  });
});

describe('mountsOf', () => {
  it('mounts every part for the Root card of a multi-part project', () => {
    const { mounts, multi } = mountsOf(two, ROOT);
    expect(multi).toBe(true);
    expect(mounts.map((m) => [m.partId, m.name, m.rootKey, m.ref])).toEqual([
      ['main', 'Main', '/srv/shop', 'p_shop~main'],
      ['pt_api', 'Api', fileKey('srv-a', '/srv/shop'), 'p_shop~pt_api'],
    ]);
    const v = new ViewPaths(mounts, multi);
    expect(v.rootKey).toBe(ROOT_KEY);
    expect(v.label(fileKey('srv-a', '/srv/shop/x'))).toBe('/Api/x');
    expect(v.label(fileKey('local', '/srv/shop/x'))).toBe('/Main/x');
  });

  it('mounts one part otherwise, its folder clean', () => {
    expect(mountsOf(two, { partId: 'pt_api' })).toEqual({ mounts: [expect.objectContaining({ partId: 'pt_api' })], multi: false });
    const { mounts, multi } = mountsOf(one, ROOT);
    expect(multi).toBe(false);
    expect(mounts[0].root).toBe('/srv/web');
    expect(mounts[0].rootKey).toBe(fileKey('srv-a', '/srv/web'));
  });
});
