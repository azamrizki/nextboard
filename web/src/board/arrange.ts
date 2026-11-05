// Auto arrange (docs/API-board.md §2.1): lays a board out again so that nothing overlaps. Sizes
// stay, and so does what belongs together: a group moves as one, wired cards stand side by side
// with the source on the left, an orchestrator card stands in the middle of its team, a drawing on
// a card goes along with it, a locked card stays put.

import type { BoardEdge, BoardNode, NodeLayout, Op } from '../lib/types';
import { frameOf } from './groups';
import { readGroup } from './kinds';

const GAP = 40;
const ROW_GAP = 64;
/** Between the columns of wired cards: the wires need the room. */
const WIRE_GAP = 112;
const GROUP_MEMBER_GAP = 24;
/** Heights this close count as equal, so that near-equal cards keep their reading order. */
const HEIGHT_BUCKET = 40;
/** How tall wired cards may stand, against the height their area would have at the view's shape. */
const TALLEST_SHARE = 1.2;
/** Up to this many cards of one level stay in one column, so that no wire has to cross a card. */
const COLUMN_CARDS = 3;
/** Places this close in height share one line, so that rows stay straight. */
const LINE_SNAP = 64;

const DRAWINGS = new Set(['ink', 'line', 'shape', 'text']);
/** Under the star around a lead, on a line of their own: wide boards would stretch its columns. */
const TASK_BOARDS = new Set(['kanban', 'checklist']);

type Cell = [row: number, col: number];

/** Where up to 8 spokes stand around a lead: left and right first, as straight wires need least room. */
const STAR_CELLS: { hub: Cell; cells: Cell[] }[] = [
  { hub: [0, 0], cells: [] },
  { hub: [0, 0], cells: [[0, 1]] },
  { hub: [0, 1], cells: [[0, 0], [0, 2]] },
  { hub: [0, 1], cells: [[0, 0], [0, 2], [1, 1]] },
  { hub: [1, 1], cells: [[0, 1], [1, 0], [1, 2], [2, 1]] },
  { hub: [1, 1], cells: [[0, 0], [0, 2], [1, 0], [1, 2], [2, 1]] },
  { hub: [1, 1], cells: [[0, 0], [0, 2], [1, 0], [1, 2], [2, 0], [2, 2]] },
  { hub: [1, 1], cells: [[0, 0], [0, 1], [0, 2], [1, 0], [1, 2], [2, 0], [2, 2]] },
  { hub: [1, 1], cells: [[0, 0], [0, 1], [0, 2], [1, 0], [1, 2], [2, 0], [2, 1], [2, 2]] },
];

/** The cells of a star, in reading order. More spokes than 8 take a wider ring, shaped like the view. */
function starCells(n: number, aspect: number, cellW: number, cellH: number): { hub: Cell; cells: Cell[] } {
  if (n < STAR_CELLS.length) return STAR_CELLS[n];
  let best = { rows: 3, cols: 5, score: Number.POSITIVE_INFINITY };
  for (const rows of [3, 5, 7, 9]) {
    let cols = Math.ceil((n + 1) / rows);
    if (cols % 2 === 0) cols++;
    if (cols < rows) continue;
    const score = Math.abs(Math.log((cols * cellW) / (rows * cellH) / aspect));
    if (score < best.score) best = { rows, cols, score };
  }
  const hub: Cell = [(best.rows - 1) / 2, (best.cols - 1) / 2];
  const ring = (c: Cell) => Math.max(Math.abs(c[0] - hub[0]), Math.abs(c[1] - hub[1])) * 100 + Math.abs(c[0] - hub[0]) + Math.abs(c[1] - hub[1]);
  const all: Cell[] = [];
  for (let r = 0; r < best.rows; r++) for (let c = 0; c < best.cols; c++) if (r !== hub[0] || c !== hub[1]) all.push([r, c]);
  const cells = all.sort((a, b) => ring(a) - ring(b) || a[0] - b[0] || a[1] - b[1]).slice(0, n);
  return { hub, cells: cells.sort((a, b) => a[0] - b[0] || a[1] - b[1]) };
}

/**
 * A lead in the middle of what it is connected to, its task boards under it. The order is reading
 * order, and the place a spoke gets keeps it, so that arranging an arranged board changes nothing.
 */
function starUnit(hub: Block, spokes: Block[], kindOf: (id: string) => string, aspect: number): Unit {
  const isTask = (b: Block) => [...b.rel.keys()].some((id) => TASK_BOARDS.has(kindOf(id)));
  const row = (b: Block) => Math.round((b.box.y + b.box.h / 2) / HEIGHT_BUCKET);
  const reading = (a: Block, b: Block) => row(a) - row(b) || a.box.x + a.box.w / 2 - (b.box.x + b.box.w / 2);
  const sorted = spokes.filter((b) => !isTask(b)).sort(reading);
  const boards = spokes.filter(isTask).sort(reading);
  const meanW = sorted.reduce((s, b) => s + b.box.w, 0) / Math.max(1, sorted.length);
  const meanH = sorted.reduce((s, b) => s + b.box.h, 0) / Math.max(1, sorted.length);
  const { hub: at, cells } = starCells(sorted.length, aspect, meanW + WIRE_GAP, meanH + WIRE_GAP);
  const placed: { block: Block; cell: Cell }[] = [{ block: hub, cell: at }, ...sorted.map((block, i) => ({ block, cell: cells[i] }))];
  const rows = new Map<number, number>();
  const cols = new Map<number, number>();
  for (const { block, cell } of placed) {
    rows.set(cell[0], Math.max(rows.get(cell[0]) ?? 0, block.box.h));
    cols.set(cell[1], Math.max(cols.get(cell[1]) ?? 0, block.box.w));
  }
  const starts = (sizes: Map<number, number>) => {
    const out = new Map<number, number>();
    let pos = 0;
    for (const k of [...sizes.keys()].sort((a, b) => a - b)) {
      out.set(k, pos);
      pos += sizes.get(k)! + WIRE_GAP;
    }
    return { out, total: pos - WIRE_GAP };
  };
  const ys = starts(rows);
  const xs = starts(cols);
  const items = placed.map(({ block, cell }) => ({
    block,
    x: Math.round(xs.out.get(cell[1])! + (cols.get(cell[1])! - block.box.w) / 2),
    y: Math.round(ys.out.get(cell[0])! + (rows.get(cell[0])! - block.box.h) / 2),
  }));
  let height = ys.total;
  if (boards.length) {
    const width = boards.reduce((s, b) => s + b.box.w + WIRE_GAP, -WIRE_GAP);
    const hubItem = items[0];
    let x = Math.round(hubItem.x + hub.box.w / 2 - width / 2);
    const y = height + WIRE_GAP;
    for (const block of boards) {
      items.push({ block, x, y });
      x += block.box.w + WIRE_GAP;
    }
    height = y + Math.max(...boards.map((b) => b.box.h));
  }
  // a board wider than the star sticks out on both sides
  const left = Math.min(...items.map((it) => it.x));
  for (const it of items) it.x -= left;
  const set = [hub, ...spokes];
  return { w: Math.max(...items.map((it) => it.x + it.block.box.w)), h: height, x0: Math.min(...set.map((b) => b.box.x)), y0: Math.min(...set.map((b) => b.box.y)), drawing: false, items };
}

/** Nodes that move as one. */
interface Block {
  box: NodeLayout;
  /** Where each node goes, relative to the block's corner. */
  rel: Map<string, { x: number; y: number }>;
  fixed: boolean;
  drawing: boolean;
  /** A group's frame can change size when its members are put in order. */
  frame?: { id: string; w: number; h: number };
}

/** Blocks laid out together, and where each one goes inside. */
interface Unit {
  w: number;
  h: number;
  x0: number;
  y0: number;
  drawing: boolean;
  items: { block: Block; x: number; y: number }[];
}

const rectOf = (n: BoardNode): NodeLayout => ({ x: n.x, y: n.y, w: n.w, h: n.h });

const touch = (a: NodeLayout, b: NodeLayout, margin = 0) => a.x < b.x + b.w + margin && a.x + a.w + margin > b.x && a.y < b.y + b.h + margin && a.y + a.h + margin > b.y;

const inside = (inner: NodeLayout, outer: NodeLayout) => inner.x >= outer.x && inner.y >= outer.y && inner.x + inner.w <= outer.x + outer.w && inner.y + inner.h <= outer.y + outer.h;

const centerIn = (n: NodeLayout, box: NodeLayout) => {
  const cx = n.x + n.w / 2;
  const cy = n.y + n.h / 2;
  return cx >= box.x && cx <= box.x + box.w && cy >= box.y && cy <= box.y + box.h;
};

const byReading = (a: NodeLayout, b: NodeLayout) => a.y - b.y || a.x - b.x;

/** Members in rows of about equal length, when they overlap; left alone when they do not. */
function memberRects(members: BoardNode[]): Map<string, NodeLayout> {
  const out = new Map(members.map((m) => [m.id, rectOf(m)]));
  const overlapping = members.some((a, i) => members.slice(i + 1).some((b) => touch(rectOf(a), rectOf(b))));
  if (!overlapping) return out;
  const sorted = [...members].sort(byReading);
  const cols = Math.ceil(Math.sqrt(sorted.length));
  let y = Math.min(...members.map((m) => m.y));
  const left = Math.min(...members.map((m) => m.x));
  for (let i = 0; i < sorted.length; i += cols) {
    const row = sorted.slice(i, i + cols);
    let x = left;
    for (const m of row) {
      out.set(m.id, { x, y, w: m.w, h: m.h });
      x += m.w + GROUP_MEMBER_GAP;
    }
    y += Math.max(...row.map((m) => m.h)) + GROUP_MEMBER_GAP;
  }
  return out;
}

/** `frames` gets the new size of a group that went into a shape's block. */
function buildBlocks(nodes: BoardNode[], frames: Map<string, { w: number; h: number }>): Block[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const inGroup = new Set<string>();
  const blocks: Block[] = [];
  for (const g of nodes) {
    if (g.kind !== 'group') continue;
    const members = readGroup(g).members.map((id) => byId.get(id)).filter((m): m is BoardNode => !!m);
    members.forEach((m) => inGroup.add(m.id));
    const rects = memberRects(members);
    const box = frameOf([...rects.values()]) ?? rectOf(g);
    const rel = new Map([[g.id, { x: 0, y: 0 }]]);
    for (const [id, r] of rects) rel.set(id, { x: r.x - box.x, y: r.y - box.y });
    blocks.push({ box, rel, fixed: !!g.locked || members.some((m) => m.locked), drawing: false, frame: { id: g.id, w: box.w, h: box.h } });
  }
  const drawings: BoardNode[] = [];
  for (const n of nodes) {
    if (n.kind === 'group' || inGroup.has(n.id)) continue;
    if (DRAWINGS.has(n.kind) && !n.locked) {
      drawings.push(n);
      continue;
    }
    blocks.push({ box: rectOf(n), rel: new Map([[n.id, { x: 0, y: 0 }]]), fixed: !!n.locked, drawing: DRAWINGS.has(n.kind) });
  }

  // a shape drawn around cards stays around them
  let rest: BoardNode[] = [];
  for (const d of [...drawings].sort((a, b) => b.w * b.h - a.w * a.h)) {
    const box = rectOf(d);
    const within = d.kind === 'shape' ? blocks.filter((b) => !b.fixed && !b.drawing && inside(b.box, box)) : [];
    if (!within.length) {
      rest.push(d);
      continue;
    }
    const rel = new Map([[d.id, { x: 0, y: 0 }]]);
    for (const b of within) {
      blocks.splice(blocks.indexOf(b), 1);
      for (const [id, p] of b.rel) rel.set(id, { x: b.box.x + p.x - box.x, y: b.box.y + p.y - box.y });
    }
    blocks.push({ box, rel, fixed: false, drawing: false });
    for (const b of within) if (b.frame) frames.set(b.frame.id, { w: b.frame.w, h: b.frame.h });
  }

  // a drawing on a card goes along with that card
  const loose: BoardNode[] = [];
  for (const d of rest) {
    const box = rectOf(d);
    const on = blocks
      .filter((b) => !b.fixed && !b.drawing && centerIn(box, b.box))
      .sort((a, b) => a.box.w * a.box.h - b.box.w * b.box.h)[0];
    if (on) on.rel.set(d.id, { x: d.x - on.box.x, y: d.y - on.box.y });
    else loose.push(d);
  }
  rest = loose;

  // loose drawings that touch form one sketch
  const parent = new Map(rest.map((d) => [d.id, d.id]));
  const find = (id: string): string => {
    const p = parent.get(id)!;
    return p === id ? id : find(p);
  };
  rest.forEach((a, i) => rest.slice(i + 1).forEach((b) => touch(rectOf(a), rectOf(b)) && parent.set(find(a.id), find(b.id))));
  const sketches = new Map<string, BoardNode[]>();
  for (const d of rest) sketches.set(find(d.id), [...(sketches.get(find(d.id)) ?? []), d]);
  for (const list of sketches.values()) {
    const x = Math.min(...list.map((d) => d.x));
    const y = Math.min(...list.map((d) => d.y));
    const box = { x, y, w: Math.max(...list.map((d) => d.x + d.w)) - x, h: Math.max(...list.map((d) => d.y + d.h)) - y };
    blocks.push({ box, rel: new Map(list.map((d) => [d.id, { x: d.x - x, y: d.y - y }])), fixed: false, drawing: true });
  }
  return blocks;
}

/** Wired blocks in columns from source to target; everything else on its own. */
function buildUnits(blocks: Block[], edges: BoardEdge[], aspect: number, kindOf: (id: string) => string): Unit[] {
  const moving = blocks.filter((b) => !b.fixed);
  const blockOf = new Map<string, Block>();
  for (const b of moving) for (const id of b.rel.keys()) blockOf.set(id, b);
  const links = edges.flatMap((e) => {
    const from = blockOf.get(e.source);
    const to = blockOf.get(e.target);
    return from && to && from !== to ? [{ from, to }] : [];
  });

  const parent = new Map(moving.map((b) => [b, b]));
  const find = (b: Block): Block => {
    const p = parent.get(b)!;
    return p === b ? b : find(p);
  };
  for (const l of links) parent.set(find(l.from), find(l.to));
  const sets = new Map<Block, Block[]>();
  for (const b of moving) sets.set(find(b), [...(sets.get(find(b)) ?? []), b]);

  const units: Unit[] = [];
  for (const set of sets.values()) {
    if (set.length === 1) {
      const b = set[0];
      units.push({ w: b.box.w, h: b.box.h, x0: b.box.x, y0: b.box.y, drawing: b.drawing, items: [{ block: b, x: 0, y: 0 }] });
      continue;
    }
    const own = links.filter((l) => set.includes(l.from));
    // a lead stands in the middle, the one with most connections if there are several
    const leads = set.filter((b) => [...b.rel.keys()].some((id) => kindOf(id) === 'orchestrator'));
    if (leads.length) {
      const degree = (b: Block) => own.filter((l) => l.from === b || l.to === b).length;
      const hub = [...leads].sort((a, b) => degree(b) - degree(a) || byReading(a.box, b.box))[0];
      units.push(starUnit(hub, set.filter((b) => b !== hub), kindOf, aspect));
      continue;
    }
    const depth = new Map(set.map((b) => [b, 0]));
    // a cycle stops growing here: wires may not form one, this only keeps it safe
    for (let i = 0; i < set.length; i++) {
      for (const l of own) if (depth.get(l.from)! + 1 > depth.get(l.to)! && depth.get(l.from)! + 1 < set.length) depth.set(l.to, depth.get(l.from)! + 1);
    }
    const levels = [...new Set(depth.values())].sort((a, b) => a - b);
    const placedAt = new Map<Block, number>();
    const columns = levels.map((level) => {
      const col = set.filter((b) => depth.get(b) === level);
      const mean = (b: Block) => {
        const from = own.filter((l) => l.to === b && placedAt.has(l.from)).map((l) => placedAt.get(l.from)!);
        return from.length ? from.reduce((s, v) => s + v, 0) / from.length : Number.POSITIVE_INFINITY;
      };
      col.sort((a, b) => mean(a) - mean(b) || byReading(a.box, b.box));
      col.forEach((b, i) => placedAt.set(b, i));
      return col;
    });
    // many cards on one level would make one very tall column: they go in rows instead, in the
    // same order, which is also the order they are read in when arranging again
    const area = set.reduce((s, b) => s + (b.box.w + GAP) * (b.box.h + GAP), 0);
    const maxH = Math.sqrt(area / aspect) * TALLEST_SHARE;
    const levelGrids = columns.map((col) => {
      const total = col.reduce((s, b) => s + b.box.h + GAP, -GAP);
      const across = col.length > COLUMN_CARDS ? Math.min(col.length, Math.max(1, Math.ceil(total / maxH))) : 1;
      const widths: number[] = Array(across).fill(0);
      const heights: number[] = Array(Math.ceil(col.length / across)).fill(0);
      col.forEach((b, i) => {
        widths[i % across] = Math.max(widths[i % across], b.box.w);
        heights[Math.floor(i / across)] = Math.max(heights[Math.floor(i / across)], b.box.h);
      });
      const sum = (list: number[]) => list.reduce((s, v) => s + v + GAP, -GAP);
      const before = (list: number[], i: number) => list.slice(0, i).reduce((s, v) => s + v + GAP, 0);
      return { col, across, widths, heights, w: sum(widths), h: sum(heights), before };
    });
    const h = Math.max(...levelGrids.map((g) => g.h));
    const items: Unit['items'] = [];
    let x = 0;
    for (const g of levelGrids) {
      const top = Math.round((h - g.h) / 2);
      g.col.forEach((block, i) => {
        items.push({ block, x: x + g.before(g.widths, i % g.across), y: top + g.before(g.heights, Math.floor(i / g.across)) });
      });
      x += g.w + WIRE_GAP;
    }
    units.push({ w: x - WIRE_GAP, h, x0: Math.min(...set.map((b) => b.box.x)), y0: Math.min(...set.map((b) => b.box.y)), drawing: false, items });
  }
  return units;
}

/**
 * The ops that arrange a board; empty when it is arranged already. `aspect` is the view's
 * width over its height, so that the result fits a screen without shrinking too far.
 */
export function arrangeOps(nodes: BoardNode[], edges: BoardEdge[], aspect: number): Op[] {
  if (!nodes.length) return [];
  const frames = new Map<string, { w: number; h: number }>();
  const blocks = buildBlocks(nodes, frames);
  const fixed = blocks.filter((b) => b.fixed).map((b) => b.box);
  const shape = Math.min(Math.max(aspect, 1), 2.4);
  const kinds = new Map(nodes.map((n) => [n.id, n.kind]));
  const units = buildUnits(blocks, edges, shape, (id) => kinds.get(id) ?? '');
  if (!units.length) return [];

  // Sizes decide the order and reading order only breaks ties, so that arranging an arranged
  // board changes nothing. Drawings come last and take what room is left.
  units.sort(
    (a, b) =>
      Number(a.drawing) - Number(b.drawing) ||
      Math.floor(b.h / HEIGHT_BUCKET) - Math.floor(a.h / HEIGHT_BUCKET) ||
      b.w - a.w ||
      a.y0 - b.y0 ||
      a.x0 - b.x0,
  );

  const area = units.reduce((s, u) => s + (u.w + GAP) * (u.h + ROW_GAP), 0);
  const limit = Math.max(Math.max(...units.map((u) => u.w)), Math.sqrt(area * shape) * 1.08);
  const ox = Math.round(Math.min(...nodes.map((n) => n.x)));
  const oy = Math.round(Math.min(...nodes.map((n) => n.y)));

  // Each unit takes the highest free place, then the leftmost: beside what stands there or under
  // it. That makes rows when cards are alike, and fills the room beside a tall one when not. A
  // place a little above another one moves down to that line, so that rows stay straight.
  const taken: NodeLayout[] = [...fixed];
  const spots = [{ x: ox, y: oy }, ...fixed.flatMap((f) => [{ x: f.x + f.w + GAP, y: f.y }, { x: ox, y: f.y + f.h + ROW_GAP }])];
  const places = new Map<Block, { x: number; y: number }>();
  for (const u of units) {
    let best: { x: number; y: number } | null = null;
    const free = (x: number, y: number) => !taken.some((t) => touch({ x, y, w: u.w, h: u.h }, t, GAP - 1));
    for (const c of spots) {
      if (c.x > ox && c.x + u.w > ox + limit) continue;
      const line = Math.max(...spots.filter((s) => s.y >= c.y && s.y - c.y < LINE_SNAP).map((s) => s.y));
      const y = free(c.x, line) ? line : c.y;
      if (best && (y > best.y || (y === best.y && c.x >= best.x))) continue;
      if (free(c.x, y)) best = { x: c.x, y };
    }
    const at = best ?? { x: ox, y: Math.max(...taken.map((t) => t.y + t.h)) + ROW_GAP };
    for (const it of u.items) places.set(it.block, { x: at.x + it.x, y: at.y + it.y });
    taken.push({ x: at.x, y: at.y, w: u.w, h: u.h });
    spots.push({ x: at.x + u.w + GAP, y: at.y }, { x: at.x, y: at.y + u.h + ROW_GAP }, { x: ox, y: at.y + u.h + ROW_GAP });
  }

  const byId = new Map(nodes.map((n) => [n.id, n]));
  const ops: Op[] = [];
  for (const [block, at] of places) {
    for (const [id, p] of block.rel) {
      const n = byId.get(id);
      if (!n) continue;
      const nx = Math.round(at.x + p.x);
      const ny = Math.round(at.y + p.y);
      const size = block.frame?.id === id ? { w: block.frame.w, h: block.frame.h } : frames.get(id);
      const sized = !!size && (size.w !== n.w || size.h !== n.h);
      if (nx === n.x && ny === n.y && !sized) continue;
      ops.push({ op: 'patch', id, set: sized ? { x: nx, y: ny, w: size.w, h: size.h } : { x: nx, y: ny } });
    }
  }
  return ops;
}
