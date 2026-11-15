// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import { ROOT } from './parts';
import {
  commonPlacement,
  defaultPlacement,
  focusedPlacement,
  isMultiPart,
  lastPlacement,
  machineOfPlace,
  orchPlace,
  placeFromValue,
  placeLabel,
  placementOfNode,
  placeValue,
  rememberPlacement,
  ROOT_VALUE,
  sessionPlace,
} from './placement';
import type { BoardNode, Project } from './types';

const project = (parts: Project['parts']): Project => ({
  id: 'p_shop',
  name: 'acme-shop',
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
  { id: 'pt_admin', name: 'admin', machineId: 'srv-a', path: '/srv/admin' },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/home/me/web' },
]);
const one = project([{ id: 'pt_shop', name: 'shop', machineId: 'local', path: '/home/me/shop' }]);

const node = (id: string, kind: string, data: Record<string, unknown> = {}): BoardNode => ({ id, kind, x: 0, y: 0, w: 100, h: 100, data });

const sessions = {
  s_root: { root: true, partId: '' },
  s_admin: { partId: 'pt_admin' },
  s_web: { partId: 'pt_web' },
  s_none: { partId: '' },
};

afterEach(() => window.localStorage.clear());

describe('placement values and wire forms', () => {
  it('round-trips the root and a part through one string', () => {
    expect(placeValue(ROOT)).toBe(ROOT_VALUE);
    expect(placeValue({ partId: 'pt_admin' })).toBe('pt_admin');
    expect(placeFromValue(ROOT_VALUE)).toEqual(ROOT);
    expect(placeFromValue('pt_admin')).toEqual({ partId: 'pt_admin' });
  });
  it('writes the sessions and orchestration forms of contracts.md', () => {
    expect(sessionPlace(ROOT)).toEqual({ root: true });
    expect(sessionPlace({ partId: 'pt_web' })).toEqual({ partId: 'pt_web' });
    expect(orchPlace(ROOT)).toEqual({ root: true });
    expect(orchPlace({ partId: 'pt_web' })).toEqual({ part: 'pt_web' });
  });
  it('labels and machines: the root is on This PC', () => {
    expect(placeLabel(two, ROOT)).toBe('Root (all parts)');
    expect(placeLabel(two, { partId: 'pt_admin' })).toBe('admin');
    expect(machineOfPlace(two, ROOT)).toBe('local');
    expect(machineOfPlace(two, { partId: 'pt_admin' })).toBe('srv-a');
    expect(isMultiPart(two)).toBe(true);
    expect(isMultiPart(one)).toBe(false);
  });
});

describe('the placement of a tile', () => {
  it('reads consoles, editor cards, file cards, web tiles and groups', () => {
    expect(placementOfNode(node('a', 'terminal', { sessionId: 's_root' }), two, sessions)).toEqual(ROOT);
    expect(placementOfNode(node('a', 'terminal', { sessionId: 's_admin' }), two, sessions)).toEqual({ partId: 'pt_admin' });
    // a console in no part's folder says nothing: never the first part
    expect(placementOfNode(node('a', 'terminal', { sessionId: 's_none' }), two, sessions)).toBeNull();
    expect(placementOfNode(node('e', 'editor', {}), two, sessions)).toEqual(ROOT);
    expect(placementOfNode(node('e', 'editor', { part: 'pt_web' }), two, sessions)).toEqual({ partId: 'pt_web' });
    // the same path on two machines: the card's machine decides
    expect(placementOfNode(node('f', 'file', { machineId: 'srv-a', path: '/srv/admin/app.php' }), two, sessions)).toEqual({ partId: 'pt_admin' });
    expect(placementOfNode(node('f', 'file', { machineId: 'local', path: '/srv/admin/app.php' }), two, sessions)).toBeNull();
    expect(placementOfNode(node('w', 'web', { partId: 'pt_web' }), two, sessions)).toEqual({ partId: 'pt_web' });
    expect(placementOfNode(node('w', 'web', { partId: 'pt_gone' }), two, sessions)).toBeNull();
    expect(placementOfNode(node('n', 'note'), two, sessions)).toBeNull();
  });
  it('gives a group the placement all its members share', () => {
    const nodes = [node('a', 'terminal', { sessionId: 's_admin' }), node('b', 'terminal', { sessionId: 's_admin' }), node('c', 'terminal', { sessionId: 's_web' })];
    expect(placementOfNode(node('g', 'group', { members: ['a', 'b'] }), two, sessions, nodes)).toEqual({ partId: 'pt_admin' });
    expect(placementOfNode(node('g', 'group', { members: ['a', 'c'] }), two, sessions, nodes)).toBeNull();
    expect(commonPlacement([nodes[0], undefined, nodes[1]], two, sessions)).toEqual({ partId: 'pt_admin' });
  });
});

describe('the default "Place in"', () => {
  const nodes = [node('a', 'terminal', { sessionId: 's_admin' }), node('n', 'note'), node('r', 'terminal', { sessionId: 's_root' })];
  it('follows the focused card, then the selection', () => {
    expect(focusedPlacement(two, nodes, [], 'r', sessions)).toEqual(ROOT);
    expect(focusedPlacement(two, nodes, ['n', 'a'], null, sessions)).toEqual({ partId: 'pt_admin' });
    expect(focusedPlacement(two, nodes, ['n'], null, sessions)).toBeNull();
  });
  it('is the focused tile’s, else the last used, else the root', () => {
    expect(defaultPlacement(two, { partId: 'pt_admin' }, ROOT)).toEqual({ partId: 'pt_admin' });
    expect(defaultPlacement(two, null, { partId: 'pt_web' })).toEqual({ partId: 'pt_web' });
    expect(defaultPlacement(two, null, null)).toEqual(ROOT);
    // a single-part project has one placement: its part
    expect(defaultPlacement(one, null, null)).toEqual({ partId: 'pt_shop' });
  });
  it('remembers the last used per project, and forgets a part that went', () => {
    expect(lastPlacement(two)).toBeNull();
    rememberPlacement(two, { partId: 'pt_web' });
    expect(lastPlacement(two)).toEqual({ partId: 'pt_web' });
    rememberPlacement(two, ROOT);
    expect(lastPlacement(two)).toEqual(ROOT);
    rememberPlacement(two, { partId: 'pt_web' });
    const without = project([two.parts![0], { id: 'pt_docs', name: 'docs', machineId: 'local', path: '/home/me/docs' }]);
    expect(lastPlacement(without)).toBeNull();
    // a single-part project keeps nothing
    rememberPlacement(one, ROOT);
    expect(lastPlacement(one)).toBeNull();
  });
});
