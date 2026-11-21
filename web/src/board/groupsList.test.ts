// @vitest-environment jsdom
// Group membership from a list, as the Code view makes it (DESIGN.md §3.19: Code view = Board view).
import { describe, expect, it } from 'vitest';
import type { BoardNode, Op } from '../lib/types';
import { addToGroupOps, GROUP_MAX, GROUP_PAD, newGroupOps, removeFromGroupOps } from './groups';

const term = (id: string, x: number, y = 0): BoardNode => ({ id, kind: 'terminal', x, y, w: 400, h: 300, z: 0, data: { sessionId: `s_${id}` } }) as BoardNode;
const group = (id: string, members: string[], name = 'Group 1'): BoardNode => ({ id, kind: 'group', x: 0, y: 0, w: 10, h: 10, z: 0, data: { name, role: 'general', members } }) as BoardNode;

/** The nodes after the ops (patches merged, puts added). */
function apply(nodes: BoardNode[], ops: Op[]): BoardNode[] {
  let out = nodes.map((n) => ({ ...n, data: { ...n.data } }));
  for (const op of ops) {
    if (op.op === 'put') out.push(op.node);
    if (op.op === 'patch')
      out = out.map((n) => (n.id === op.id ? { ...n, ...op.set, data: op.set.data ? { ...n.data, ...op.set.data } : n.data } : n)) as BoardNode[];
  }
  return out;
}
const membersOf = (nodes: BoardNode[], id: string) => nodes.find((n) => n.id === id)?.data.members;
const byId = (nodes: BoardNode[], id: string) => nodes.find((n) => n.id === id)!;

describe('adding a console to a group from a list', () => {
  it('moves it beside the last member and grows the frame', () => {
    const nodes = [term('a', 0), term('b', 2000, 900), group('g', ['a'])];
    const after = apply(nodes, addToGroupOps(nodes, 'g', 'b')!);
    expect(membersOf(after, 'g')).toEqual(['a', 'b']);
    expect(byId(after, 'b')).toMatchObject({ x: 400 + GROUP_PAD, y: 0 });
    const g = byId(after, 'g');
    expect(g.x).toBeLessThanOrEqual(0);
    expect(g.x + g.w).toBeGreaterThanOrEqual(400 + GROUP_PAD + 400);
  });

  it('takes it out of the group it was in', () => {
    const nodes = [term('a', 0), term('b', 500), term('c', 1000), group('g1', ['a', 'b']), group('g2', ['c'], 'Group 2')];
    const after = apply(nodes, addToGroupOps(nodes, 'g2', 'b')!);
    expect(membersOf(after, 'g1')).toEqual(['a']);
    expect(membersOf(after, 'g2')).toEqual(['c', 'b']);
  });

  it('refuses a member, a full group and what is not a console', () => {
    const nodes = [term('a', 0), group('g', ['a']), { ...term('n', 0), kind: 'note' } as BoardNode];
    expect(addToGroupOps(nodes, 'g', 'a')).toBeNull();
    expect(addToGroupOps(nodes, 'g', 'n')).toBeNull();
    const many = Array.from({ length: GROUP_MAX }, (_, i) => term(`t${i}`, i * 500));
    const full = [...many, term('x', 0, 2000), group('g', many.map((t) => t.id))];
    expect(addToGroupOps(full, 'g', 'x')).toBeNull();
  });
});

describe('taking a console out of its group', () => {
  it('leaves the rest and shrinks the frame to them', () => {
    const nodes = [term('a', 0), term('b', 500), group('g', ['a', 'b'])];
    const after = apply(nodes, removeFromGroupOps(nodes, 'b'));
    expect(membersOf(after, 'g')).toEqual(['a']);
    expect(byId(after, 'g').x + byId(after, 'g').w).toBeLessThan(500);
  });

  it('does nothing for a console in no group', () => {
    expect(removeFromGroupOps([term('a', 0)], 'a')).toEqual([]);
  });
});

describe('a new group of consoles picked from a list', () => {
  it('lines them up at the spot and frames them', () => {
    const nodes = [term('a', 0, 0), term('b', 3000, 2000), term('c', 100, 900)];
    const made = newGroupOps(nodes, ['a', 'b'], { x: 5000, y: 100 }, { name: 'Review', role: 'reviewer' })!;
    const after = apply(nodes, made.ops);
    expect(byId(after, 'a')).toMatchObject({ x: 5000, y: 100 });
    expect(byId(after, 'b')).toMatchObject({ x: 5000 + 400 + GROUP_PAD, y: 100 });
    expect(byId(after, 'c')).toMatchObject({ x: 100, y: 900 });
    expect(made.group.data).toMatchObject({ name: 'Review', role: 'reviewer', members: ['a', 'b'] });
    expect(made.group.x).toBeLessThan(5000);
    expect(made.group.x + made.group.w).toBeGreaterThan(5000 + 2 * 400 + GROUP_PAD);
  });

  it('needs a console', () => {
    expect(newGroupOps([term('a', 0)], [], { x: 0, y: 0 })).toBeNull();
    expect(newGroupOps([term('a', 0)], ['zz'], { x: 0, y: 0 })).toBeNull();
  });
});
