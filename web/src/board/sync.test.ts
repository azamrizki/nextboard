// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import type { BoardNode, Op } from '../lib/types';
import { applyOps, indexNodes } from './ops';
import { BoardSync } from './sync';

const node = (id: string, x = 0): BoardNode => ({ id, kind: 'note', x, y: 0, w: 100, h: 100, z: 0, data: {} }) as BoardNode;

function ready(nodes: BoardNode[]): BoardSync {
  const s = new BoardSync('ws');
  s.reset({ nodes, edges: [], rev: 1 } as never);
  return s;
}

describe('applyOps with a known index', () => {
  it('gives the same result as without one and leaves the index alone', () => {
    const state = { nodes: [node('a'), node('b'), node('c')], edges: [] };
    const index = indexNodes(state.nodes);
    const ops: Op[] = [
      { op: 'patch', id: 'b', set: { x: 5 } },
      { op: 'put', node: node('d', 9) },
      { op: 'patch', id: 'd', set: { x: 10 } },
      { op: 'z', id: 'a', to: 'front' },
      { op: 'patch', id: 'a', set: { x: 7 } },
      { op: 'del', id: 'c' },
    ];
    expect(applyOps(state, ops, index)).toEqual(applyOps(state, ops));
    expect([...index]).toEqual([
      ['a', 0],
      ['b', 1],
      ['c', 2],
    ]);
  });
});

describe('BoardSync.node', () => {
  it('follows patches, puts, deletes and reorders', () => {
    const s = ready([node('a'), node('b')]);
    expect(s.node('b')?.x).toBe(0);
    s.change([{ op: 'patch', id: 'b', set: { x: 40 } }]);
    expect(s.node('b')).toBe(s.state.nodes[1]);
    expect(s.node('b')?.x).toBe(40);
    s.change([{ op: 'put', node: node('c', 3) }]);
    expect(s.node('c')?.x).toBe(3);
    s.change([{ op: 'z', id: 'c', to: 'back' }]);
    s.change([{ op: 'patch', id: 'a', set: { x: 1 } }]);
    for (const n of s.state.nodes) expect(s.node(n.id)).toBe(n);
    s.change([{ op: 'del', id: 'a' }]);
    expect(s.node('a')).toBeUndefined();
    expect(s.node('b')?.x).toBe(40);
    s.dispose();
  });

  it('follows a state replaced from the hub', () => {
    const s = ready([node('a')]);
    expect(s.node('a')).toBeDefined();
    s.reset({ nodes: [node('x', 2)], edges: [], rev: 5 } as never);
    expect(s.node('a')).toBeUndefined();
    expect(s.node('x')?.x).toBe(2);
    s.dispose();
  });
});
