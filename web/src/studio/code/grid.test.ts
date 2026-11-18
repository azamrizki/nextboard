// The editor's split grid (grid.ts): at most two across and two down, four in all; splits,
// removal, sizes, neighbours, the stored form and the row of before.

import { describe, expect, it } from 'vitest';
import { canSplit, extent, fits, fromRow, fromSaved, leaf, leaves, neighbour, plan, prune, readSaved, removeLeaf, resize, splitLeaf, toSaved, type GridNode } from './grid';

const must = (n: GridNode | null): GridNode => {
  expect(n).not.toBeNull();
  return n!;
};

describe('splitting', () => {
  it('puts the new group on the side asked for', () => {
    expect(splitLeaf(leaf('a'), 'a', 'right', 'b')).toEqual({ dir: 'row', a: leaf('a'), b: leaf('b'), at: 0.5 });
    expect(splitLeaf(leaf('a'), 'a', 'left', 'b')).toEqual({ dir: 'row', a: leaf('b'), b: leaf('a'), at: 0.5 });
    expect(splitLeaf(leaf('a'), 'a', 'bottom', 'b')).toEqual({ dir: 'col', a: leaf('a'), b: leaf('b'), at: 0.5 });
    expect(splitLeaf(leaf('a'), 'a', 'top', 'b')).toEqual({ dir: 'col', a: leaf('b'), b: leaf('a'), at: 0.5 });
  });

  it('never makes a third column or row', () => {
    const two = must(splitLeaf(leaf('a'), 'a', 'right', 'b'));
    expect(splitLeaf(two, 'a', 'right', 'c')).toBeNull();
    expect(splitLeaf(two, 'b', 'left', 'c')).toBeNull();
    expect(canSplit(two, 'b', 'right')).toBe(false);
    // down is fine: one side of the row becomes a column
    const three = must(splitLeaf(two, 'b', 'bottom', 'c'));
    expect(three).toEqual({ dir: 'row', a: leaf('a'), b: { dir: 'col', a: leaf('b'), b: leaf('c'), at: 0.5 }, at: 0.5 });
    expect(leaves(three)).toEqual(['a', 'b', 'c']);
    expect(extent(three)).toEqual({ rows: 2, cols: 2 });
    expect(splitLeaf(three, 'c', 'bottom', 'd')).toBeNull();
  });

  it('fills a 2×2 grid, and no more', () => {
    const three = must(splitLeaf(must(splitLeaf(leaf('a'), 'a', 'right', 'b')), 'b', 'bottom', 'c'));
    const four = must(splitLeaf(three, 'a', 'bottom', 'd'));
    expect(leaves(four)).toEqual(['a', 'd', 'b', 'c']);
    expect(extent(four)).toEqual({ rows: 2, cols: 2 });
    for (const id of leaves(four)) for (const e of ['left', 'right', 'top', 'bottom'] as const) expect(canSplit(four, id, e)).toBe(false);
  });

  it('splits one group of a stacked pair sideways (a row inside a column)', () => {
    const stacked = must(splitLeaf(leaf('a'), 'a', 'bottom', 'b'));
    const next = must(splitLeaf(stacked, 'a', 'right', 'c'));
    expect(next).toEqual({ dir: 'col', a: { dir: 'row', a: leaf('a'), b: leaf('c'), at: 0.5 }, b: leaf('b'), at: 0.5 });
    expect(fits(next)).toBe(true);
  });

  it('refuses a group it does not hold', () => {
    expect(splitLeaf(leaf('a'), 'x', 'right', 'b')).toBeNull();
  });
});

describe('removing, pruning and resizing', () => {
  const three: GridNode = { dir: 'row', a: leaf('a'), b: { dir: 'col', a: leaf('b'), b: leaf('c'), at: 0.3 }, at: 0.6 };

  it('gives a removed group’s place to its sibling', () => {
    expect(removeLeaf(three, 'c')).toEqual({ dir: 'row', a: leaf('a'), b: leaf('b'), at: 0.6 });
    expect(removeLeaf(three, 'a')).toEqual({ dir: 'col', a: leaf('b'), b: leaf('c'), at: 0.3 });
    expect(removeLeaf(leaf('a'), 'a')).toEqual(leaf('a'));
  });

  it('keeps only the groups asked for', () => {
    expect(prune(three, new Set(['a', 'c']))).toEqual({ dir: 'row', a: leaf('a'), b: leaf('c'), at: 0.6 });
    expect(prune(three, new Set())).toBeNull();
  });

  it('resizes the split at a path, within 10–90 %', () => {
    expect(resize(three, '', 0.25)).toMatchObject({ at: 0.25 });
    expect(resize(three, 'b', 0.95)).toMatchObject({ b: { at: 0.9 } });
    expect(resize(three, 'b', -1)).toMatchObject({ b: { at: 0.1 } });
  });
});

describe('neighbours', () => {
  const four: GridNode = { dir: 'row', a: { dir: 'col', a: leaf('a'), b: leaf('b'), at: 0.5 }, b: { dir: 'col', a: leaf('c'), b: leaf('d'), at: 0.5 }, at: 0.5 };
  it('finds the group on a side, the top or left one across a split', () => {
    expect(neighbour(four, 'a', 'right')).toBe('c');
    expect(neighbour(four, 'd', 'left')).toBe('a');
    expect(neighbour(four, 'a', 'bottom')).toBe('b');
    expect(neighbour(four, 'b', 'top')).toBe('a');
    expect(neighbour(four, 'a', 'left')).toBeNull();
    expect(neighbour(four, 'c', 'top')).toBeNull();
    expect(neighbour(leaf('a'), 'a', 'right')).toBeNull();
  });
});

describe('stored layouts', () => {
  it('a row of before: two side by side, a third under the second', () => {
    expect(fromRow(['a'])).toEqual(leaf('a'));
    expect(fromRow(['a', 'b'])).toEqual({ dir: 'row', a: leaf('a'), b: leaf('b'), at: 0.5 });
    expect(fromRow(['a', 'b', 'c'])).toEqual({ dir: 'row', a: leaf('a'), b: { dir: 'col', a: leaf('b'), b: leaf('c'), at: 0.5 }, at: 0.5 });
    expect(leaves(fromRow(['a', 'b', 'c', 'd']))).toEqual(['a', 'c', 'b', 'd']);
    expect(fits(fromRow(['a', 'b', 'c', 'd', 'e']))).toBe(true);
  });

  it('goes to indices and back', () => {
    const n: GridNode = { dir: 'col', a: { dir: 'row', a: leaf('g3'), b: leaf('g1'), at: 0.333333 }, b: leaf('g2'), at: 0.5 };
    const ids = ['g3', 'g1', 'g2'];
    const saved = toSaved(n, ids);
    expect(saved).toEqual({ dir: 'col', a: { dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.333 }, b: { g: 2 }, at: 0.5 });
    expect(readSaved(JSON.parse(JSON.stringify(saved)), 3)).toEqual(saved);
    expect(fromSaved(saved, ['x', 'y', 'z'])).toEqual({ dir: 'col', a: { dir: 'row', a: leaf('x'), b: leaf('y'), at: 0.333 }, b: leaf('z'), at: 0.5 });
  });

  it('refuses a stored layout that does not match its groups or the grid', () => {
    const row3 = { dir: 'row', a: { g: 0 }, b: { dir: 'row', a: { g: 1 }, b: { g: 2 }, at: 0.5 }, at: 0.5 };
    expect(readSaved(row3, 3)).toBeNull(); // three across
    expect(readSaved({ dir: 'row', a: { g: 0 }, b: { g: 0 }, at: 0.5 }, 2)).toBeNull(); // one group twice
    expect(readSaved({ dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 0.5 }, 3)).toBeNull(); // a group left out
    expect(readSaved({ dir: 'diagonal', a: { g: 0 }, b: { g: 1 } }, 2)).toBeNull();
    expect(readSaved({ g: 1.5 }, 2)).toBeNull();
    expect(readSaved(null, 1)).toBeNull();
    // a share out of range is brought back in
    expect(readSaved({ dir: 'row', a: { g: 0 }, b: { g: 1 }, at: 7 }, 2)).toMatchObject({ at: 0.9 });
  });
});

describe('plan', () => {
  it('places each group and each sash as shares of the area', () => {
    const p = plan({ dir: 'row', a: leaf('a'), b: { dir: 'col', a: leaf('b'), b: leaf('c'), at: 0.25 }, at: 0.6 });
    expect(p.cells).toEqual([
      { id: 'a', x: 0, y: 0, w: 0.6, h: 1 },
      { id: 'b', x: 0.6, y: 0, w: 0.4, h: 0.25 },
      { id: 'c', x: 0.6, y: 0.25, w: 0.4, h: 0.75 },
    ]);
    expect(p.sashes.map((s) => [s.path, s.dir, s.at])).toEqual([
      ['b', 'col', 0.25],
      ['', 'row', 0.6],
    ]);
  });
});
