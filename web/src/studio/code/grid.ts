// The editor's split grid (docs/API-studio.md §7.15): up to four editor groups in at most two
// rows and two columns, as VS Code's grid but smaller. Pure: no Monaco, no DOM.
//
// A layout is a tree of splits. A "row" split puts a beside b (a on the left), a "col" split puts
// a above b; `at` is the share a takes. A leaf is a group, by id at run time and by its index in
// the saved groups when stored (persist.ts). Every tree here fits the grid: no more than two
// groups side by side or stacked anywhere, and four in all.

export type Edge = 'left' | 'right' | 'top' | 'bottom';
export type SplitDir = 'row' | 'col';

export type GridNode = { g: string } | { dir: SplitDir; a: GridNode; b: GridNode; at: number };
/** As stored: a leaf is the index of its group in the saved list. */
export type SavedGrid = { g: number } | { dir: SplitDir; a: SavedGrid; b: SavedGrid; at: number };

export const MAX_ROWS = 2;
export const MAX_COLS = 2;
export const MAX_CELLS = MAX_ROWS * MAX_COLS;
/** The smallest share a side of a split keeps when dragged. */
export const MIN_SHARE = 0.1;

export const isLeaf = <T extends { g: unknown }>(n: T | { dir: SplitDir }): n is T => 'g' in n;

export const leaf = (g: string): GridNode => ({ g });

const clampAt = (at: unknown) => (typeof at === 'number' && Number.isFinite(at) ? Math.min(1 - MIN_SHARE, Math.max(MIN_SHARE, at)) : 0.5);

/** The groups in reading order: a before b, left to right, then top to bottom within a column. */
export function leaves(n: GridNode): string[] {
  return isLeaf(n) ? [n.g] : [...leaves(n.a), ...leaves(n.b)];
}

/** How many rows and columns a layout needs. */
export function extent(n: GridNode | SavedGrid): { rows: number; cols: number } {
  if (isLeaf(n)) return { rows: 1, cols: 1 };
  const a = extent(n.a);
  const b = extent(n.b);
  return n.dir === 'row' ? { rows: Math.max(a.rows, b.rows), cols: a.cols + b.cols } : { rows: a.rows + b.rows, cols: Math.max(a.cols, b.cols) };
}

export function fits(n: GridNode | SavedGrid): boolean {
  const e = extent(n);
  return e.rows <= MAX_ROWS && e.cols <= MAX_COLS;
}

const dirOf = (edge: Edge): SplitDir => (edge === 'left' || edge === 'right' ? 'row' : 'col');
const first = (edge: Edge) => edge === 'left' || edge === 'top';

/** The layout with group `id` split at an edge, the new group `added` taking that side; null when it would not fit. */
export function splitLeaf(n: GridNode, id: string, edge: Edge, added: string): GridNode | null {
  if (leaves(n).length >= MAX_CELLS) return null;
  let found = false;
  const walk = (x: GridNode): GridNode => {
    if (isLeaf(x)) {
      if (x.g !== id) return x;
      found = true;
      const fresh = leaf(added);
      return { dir: dirOf(edge), a: first(edge) ? fresh : x, b: first(edge) ? x : fresh, at: 0.5 };
    }
    return { ...x, a: walk(x.a), b: walk(x.b) };
  };
  const next = walk(n);
  return found && fits(next) ? next : null;
}

export function canSplit(n: GridNode, id: string, edge: Edge): boolean {
  return splitLeaf(n, id, edge, '\0') !== null;
}

/** The layout without group `id`: its sibling takes the whole split. The last group stays. */
export function removeLeaf(n: GridNode, id: string): GridNode {
  if (isLeaf(n)) return n;
  if (isLeaf(n.a) && n.a.g === id) return n.b;
  if (isLeaf(n.b) && n.b.g === id) return n.a;
  return { ...n, a: removeLeaf(n.a, id), b: removeLeaf(n.b, id) };
}

/** Only the groups in `keep`, in their places (a split that lost one side becomes the other). */
export function prune(n: GridNode, keep: ReadonlySet<string>): GridNode | null {
  if (isLeaf(n)) return keep.has(n.g) ? n : null;
  const a = prune(n.a, keep);
  const b = prune(n.b, keep);
  if (!a) return b;
  if (!b) return a;
  return { ...n, a, b };
}

/** The split at a path ("" the root, then "a"/"b" per level) given a new share. */
export function resize(n: GridNode, path: string, at: number): GridNode {
  if (isLeaf(n)) return n;
  if (!path) return { ...n, at: clampAt(at) };
  return path[0] === 'a' ? { ...n, a: resize(n.a, path.slice(1), at) } : { ...n, b: resize(n.b, path.slice(1), at) };
}

/**
 * The group next to `id` on one side, in the same row or column (the one at the top or the left
 * when the other side is split); null at the grid's edge.
 */
export function neighbour(n: GridNode, id: string, edge: Edge): string | null {
  const dir = dirOf(edge);
  // the path down to the leaf, then back up to the nearest split that way with the leaf on the near side
  const trail: { node: Exclude<GridNode, { g: string }>; side: 'a' | 'b' }[] = [];
  const find = (x: GridNode): boolean => {
    if (isLeaf(x)) return x.g === id;
    trail.push({ node: x, side: 'a' });
    if (find(x.a)) return true;
    trail[trail.length - 1].side = 'b';
    if (find(x.b)) return true;
    trail.pop();
    return false;
  };
  if (!find(n)) return null;
  for (let i = trail.length - 1; i >= 0; i--) {
    const { node, side } = trail[i];
    if (node.dir !== dir || side !== (first(edge) ? 'b' : 'a')) continue;
    // the other side is one group, or two across the way (a grid has no third): its top or left one
    return leaves(first(edge) ? node.a : node.b)[0];
  }
  return null;
}

/** Groups saved before the grid (one row, at most three): two side by side, a third under the second. */
export function fromRow(ids: string[]): GridNode {
  const list = ids.slice(0, MAX_CELLS);
  if (!list.length) throw new Error('fromRow: no group');
  if (list.length === 1) return leaf(list[0]);
  if (list.length === 2) return { dir: 'row', a: leaf(list[0]), b: leaf(list[1]), at: 0.5 };
  const right: GridNode = list.length === 3 ? { dir: 'col', a: leaf(list[1]), b: leaf(list[2]), at: 0.5 } : { dir: 'col', a: leaf(list[1]), b: leaf(list[3]), at: 0.5 };
  const left: GridNode = list.length === 4 ? { dir: 'col', a: leaf(list[0]), b: leaf(list[2]), at: 0.5 } : leaf(list[0]);
  return { dir: 'row', a: left, b: right, at: 0.5 };
}

/** A layout as stored: leaves by their group's index in `ids`. */
export function toSaved(n: GridNode, ids: string[]): SavedGrid {
  if (isLeaf(n)) return { g: Math.max(0, ids.indexOf(n.g)) };
  return { dir: n.dir, a: toSaved(n.a, ids), b: toSaved(n.b, ids), at: Math.round(n.at * 1000) / 1000 };
}

/**
 * A stored layout read back for `count` saved groups, or null when it does not hold each of them
 * exactly once, or does not fit the grid (edited by hand, or from a newer client).
 */
export function readSaved(raw: unknown, count: number): SavedGrid | null {
  const seen = new Set<number>();
  const read = (x: unknown, depth: number): SavedGrid | null => {
    if (!x || typeof x !== 'object' || depth > 4) return null;
    const o = x as Record<string, unknown>;
    if ('g' in o) {
      const g = o.g;
      if (typeof g !== 'number' || !Number.isInteger(g) || g < 0 || g >= count || seen.has(g)) return null;
      seen.add(g);
      return { g };
    }
    if (o.dir !== 'row' && o.dir !== 'col') return null;
    const a = read(o.a, depth + 1);
    const b = a && read(o.b, depth + 1);
    return a && b ? { dir: o.dir, a, b, at: clampAt(o.at) } : null;
  };
  const tree = read(raw, 0);
  return tree && seen.size === count && fits(tree) ? tree : null;
}

/** A stored layout with its leaves made group ids again. */
export function fromSaved(s: SavedGrid, ids: string[]): GridNode {
  if (isLeaf(s)) return leaf(ids[s.g]);
  return { dir: s.dir, a: fromSaved(s.a, ids), b: fromSaved(s.b, ids), at: s.at };
}

/** Where each group and each split's sash lie, as shares of the editor area (0–1). */
export interface GridBox {
  x: number;
  y: number;
  w: number;
  h: number;
}
export interface GridPlan {
  cells: (GridBox & { id: string })[];
  sashes: (GridBox & { path: string; dir: SplitDir; at: number })[];
}

export function plan(n: GridNode): GridPlan {
  const out: GridPlan = { cells: [], sashes: [] };
  const walk = (x: GridNode, box: GridBox, path: string) => {
    if (isLeaf(x)) {
      out.cells.push({ id: x.g, ...box });
      return;
    }
    if (x.dir === 'row') {
      const wa = box.w * x.at;
      walk(x.a, { ...box, w: wa }, `${path}a`);
      walk(x.b, { ...box, x: box.x + wa, w: box.w - wa }, `${path}b`);
    } else {
      const ha = box.h * x.at;
      walk(x.a, { ...box, h: ha }, `${path}a`);
      walk(x.b, { ...box, y: box.y + ha, h: box.h - ha }, `${path}b`);
    }
    out.sashes.push({ ...box, path, dir: x.dir, at: x.at });
  };
  walk(n, { x: 0, y: 0, w: 1, h: 1 }, '');
  return out;
}
