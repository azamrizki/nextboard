import { describe, expect, it } from 'vitest';
import { ROOT_VALUE } from '../lib/placement';
import type { Project } from '../lib/types';
import { deployPlaces, initialPlaces, partNamed, savedPlace, unplaced } from './layoutPlace';
import type { Layout } from './types';

const project = (parts: Project['parts']): Project => ({
  id: 'p_shop',
  name: 'acme-shop',
  machineId: 'local',
  path: '/home/me/shop',
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});
const two = project([
  { id: 'pt_admin', name: 'Admin', machineId: 'srv-a', path: '/srv/admin' },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/home/me/web' },
]);

const lane = (key: string, group: string | undefined, place: { root?: boolean; part?: string } = {}) => ({ key, group, x: 0, y: 0, w: 10, h: 10, ...place });
const layout = (lanes: Layout['lanes'], groups: string[]): Layout => ({
  id: 'l1',
  name: 'Team',
  builtin: false,
  lanes,
  groups: groups.map((key) => ({ key, name: key.toUpperCase(), role: 'general', color: 'sky' })),
  nodes: null,
  wires: null,
});

describe('saved placements matched by part name', () => {
  it('finds a part by id, else by name in any case', () => {
    expect(partNamed(two, 'pt_web')?.name).toBe('frontend');
    expect(partNamed(two, ' admin ')?.id).toBe('pt_admin');
    expect(partNamed(two, 'docs')).toBeUndefined();
    expect(savedPlace({ root: true }, two)).toBe(ROOT_VALUE);
    expect(savedPlace({ part: 'FRONTEND' }, two)).toBe('pt_web');
    expect(savedPlace({ part: 'docs' }, two)).toBe('');
    expect(savedPlace({}, two)).toBeNull();
  });

  it('places groups by what their lanes were saved in, and asks for names this project lacks', () => {
    const l = layout(
      [lane('1', 'a', { part: 'admin' }), lane('2', 'a', { part: 'admin' }), lane('3', 'b', { part: 'docs' }), lane('4', 'c'), lane('5', undefined, { root: true }), lane('6', 'a', { part: 'frontend' })],
      ['a', 'b', 'c'],
    );
    const p = initialPlaces(l, two, 'pt_web');
    expect(p.groups).toEqual({ a: 'pt_admin', b: '', c: 'pt_web' });
    // a lane saved somewhere else than its group keeps its own; a lane outside groups has one
    expect(p.lanes).toEqual({ '5': ROOT_VALUE, '6': 'pt_web' });
    expect(p.unmatched).toEqual([{ key: 'b', part: 'docs' }]);
    expect(unplaced(p.groups, p.lanes, l)).toEqual(['b']);
    // picked: nothing left to ask, and every group and own lane is sent
    const groups = { ...p.groups, b: ROOT_VALUE };
    expect(unplaced(groups, p.lanes, l)).toEqual([]);
    expect(deployPlaces(groups, p.lanes, l)).toEqual({ a: { part: 'pt_admin' }, b: { root: true }, c: { part: 'pt_web' }, '5': { root: true }, '6': { part: 'pt_web' } });
  });

  it('uses the board default for a template that saved nothing', () => {
    const l = layout([lane('1', 'a'), lane('2')], ['a']);
    expect(initialPlaces(l, two, ROOT_VALUE)).toEqual({ groups: { a: ROOT_VALUE }, lanes: { '2': ROOT_VALUE }, cards: {}, unmatched: [] });
  });

  it('places editor cards by their saved part name, else at the Root', () => {
    const l = { ...layout([], []), nodes: [
      { key: 'e1', kind: 'editor', x: 0, y: 0, w: 1, h: 1, data: { part: 'admin' } },
      { key: 'e2', kind: 'editor', x: 0, y: 0, w: 1, h: 1 },
      { key: 'e3', kind: 'editor', x: 0, y: 0, w: 1, h: 1, data: { part: 'api' } },
      { key: 'k1', kind: 'kanban', x: 0, y: 0, w: 1, h: 1 },
    ] };
    const p = initialPlaces(l, two, 'pt_web');
    expect(p.cards).toEqual({ e1: 'pt_admin', e2: ROOT_VALUE, e3: '' });
    expect(p.unmatched).toEqual([{ key: 'e3', part: 'api' }]);
    expect(unplaced(p.groups, p.lanes, l, p.cards)).toEqual(['e3']);
    expect(deployPlaces(p.groups, p.lanes, l, { ...p.cards, e3: 'pt_web' })).toEqual({ e1: { part: 'pt_admin' }, e2: { root: true }, e3: { part: 'pt_web' } });
  });

  it('lets a lane added by a group size follow its group', () => {
    const l = layout([lane('1', 'a', { part: 'x' })], ['a']);
    const p = initialPlaces(l, two, ROOT_VALUE);
    const sized = layout([lane('1', 'a', { part: 'x' }), lane('1b', 'a')], ['a']);
    expect(unplaced(p.groups, p.lanes, sized)).toEqual(['a']);
    expect(deployPlaces({ a: 'pt_admin' }, p.lanes, sized)).toEqual({ a: { part: 'pt_admin' } });
  });
});
