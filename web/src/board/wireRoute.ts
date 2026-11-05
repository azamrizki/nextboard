// Where a wire between two tiles runs. Orthogonal with rounded corners, like the grid
// it sits on: straight when the tiles face each other, one clean elbow when they do
// not, and around anything in the way, never under it. Pure geometry: no React, so it
// can be tried on thousands of layouts.

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Pt {
  x: number;
  y: number;
}

export type Side = 'left' | 'right' | 'top' | 'bottom';

export interface Route {
  /** Corners, from the port on the source to the tip on the target. */
  points: Pt[];
  /** Stops short of the tip, so the line does not poke through the arrow. */
  path: string;
  start: Pt;
  tip: Pt;
  /** Which way the arrow points, in degrees. */
  angle: number;
  /** Where a label sits: the middle of the longest run. */
  mid: Pt;
}

const RADIUS = 12; // of a corner
const CORNER = 26; // a port stays this far from a tile's corner
const CLEAR = 18; // room kept around a tile
const STUB = 30; // straight out of a side before the first bend
const TRUNK = 48; // wires leaving one side turn at the same distance, like a bus
const BEND = 70; // how much length one bend is worth avoiding
const ARROW = 10; // length of the arrow
const NEAR = 520; // tiles further than this from both ends do not matter
const MAX_OBSTACLES = 24;

const DIRS: Record<Side, Pt> = { left: { x: -1, y: 0 }, right: { x: 1, y: 0 }, top: { x: 0, y: -1 }, bottom: { x: 0, y: 1 } };
const SIDES: Side[] = ['right', 'left', 'bottom', 'top'];
const HORIZONTAL = (s: Side) => s === 'left' || s === 'right';

function port(b: Box, side: Side): Pt {
  switch (side) {
    case 'left':
      return { x: b.x, y: b.y + b.h / 2 };
    case 'right':
      return { x: b.x + b.w, y: b.y + b.h / 2 };
    case 'top':
      return { x: b.x + b.w / 2, y: b.y };
    case 'bottom':
      return { x: b.x + b.w / 2, y: b.y + b.h };
  }
}

const hits = (a: Pt, b: Pt, o: Box, pad: number) =>
  Math.max(a.x, b.x) > o.x - pad && Math.min(a.x, b.x) < o.x + o.w + pad && Math.max(a.y, b.y) > o.y - pad && Math.min(a.y, b.y) < o.y + o.h + pad;

/** A hair less than the room the search keeps, so a line it found along a tile counts as clear. */
const clearOf = (points: Pt[], obstacles: Box[]) => points.every((p, i) => i === 0 || obstacles.every((o) => !hits(points[i - 1], p, o, CLEAR - 1)));

const contains = (outer: Box, inner: Box) => outer.x <= inner.x && outer.y <= inner.y && outer.x + outer.w >= inner.x + inner.w && outer.y + outer.h >= inner.y + inner.h;

/** Which sides face each other; null when the tiles overlap. */
function facing(s: Box, t: Box): [Side, Side] | null {
  const gapX = t.x >= s.x + s.w ? t.x - (s.x + s.w) : s.x >= t.x + t.w ? s.x - (t.x + t.w) : -1;
  const gapY = t.y >= s.y + s.h ? t.y - (s.y + s.h) : s.y >= t.y + t.h ? s.y - (t.y + t.h) : -1;
  if (gapX < 0 && gapY < 0) return null;
  if (gapX >= gapY) return t.x > s.x ? ['right', 'left'] : ['left', 'right'];
  return t.y > s.y ? ['bottom', 'top'] : ['top', 'bottom'];
}

/** A straight line, when the facing sides overlap enough for one. */
function straight(s: Box, t: Box, a: Side, b: Side): Pt[] | null {
  const across = HORIZONTAL(a);
  const [s0, s1, t0, t1] = across ? [s.y, s.y + s.h, t.y, t.y + t.h] : [s.x, s.x + s.w, t.x, t.x + t.w];
  const lo = Math.max(s0, t0) + CORNER;
  const hi = Math.min(s1, t1) - CORNER;
  if (lo > hi) return null;
  const inRange = (v: number) => v >= lo && v <= hi;
  const tMid = (t0 + t1) / 2;
  const sMid = (s0 + s1) / 2;
  // The source's own middle first: that is where its knob and its other wires are.
  const at = inRange(sMid) ? sMid : inRange(tMid) ? tMid : (lo + hi) / 2;
  const from = port(s, a);
  const to = port(t, b);
  return across ? [{ x: from.x, y: at }, { x: to.x, y: at }] : [{ x: at, y: from.y }, { x: at, y: to.y }];
}

/** One elbow: out of the source, a turn near it, across, and in. */
function elbow(s: Box, t: Box, a: Side, b: Side): Pt[] {
  const from = port(s, a);
  const to = port(t, b);
  if (HORIZONTAL(a)) {
    const dir = DIRS[a].x;
    const turn = from.x + dir * Math.min(Math.abs(to.x - from.x) / 2, TRUNK);
    return [from, { x: turn, y: from.y }, { x: turn, y: to.y }, to];
  }
  const dir = DIRS[a].y;
  const turn = from.y + dir * Math.min(Math.abs(to.y - from.y) / 2, TRUNK);
  return [from, { x: from.x, y: turn }, { x: to.x, y: turn }, to];
}

function tidy(points: Pt[]): Pt[] {
  const out: Pt[] = [];
  for (const p of points) {
    const last = out[out.length - 1];
    if (last && last.x === p.x && last.y === p.y) continue;
    const prev = out[out.length - 2];
    if (prev && last && ((prev.x === last.x && last.x === p.x) || (prev.y === last.y && last.y === p.y))) out[out.length - 1] = p;
    else out.push(p);
  }
  return out;
}

const lengthOf = (points: Pt[]) => points.reduce((sum, p, i) => (i === 0 ? 0 : sum + Math.abs(p.x - points[i - 1].x) + Math.abs(p.y - points[i - 1].y)), 0);
const costOf = (points: Pt[]) => lengthOf(points) + (points.length - 2) * BEND;

/** Shortest way around, on the lines running along the tiles; bends cost extra. */
function around(s: Box, t: Box, a: Side, b: Side, obstacles: Box[]): Pt[] | null {
  const from = port(s, a);
  const to = port(t, b);
  const out = { x: from.x + DIRS[a].x * STUB, y: from.y + DIRS[a].y * STUB };
  const inn = { x: to.x + DIRS[b].x * STUB, y: to.y + DIRS[b].y * STUB };
  const walls = [s, t, ...obstacles];
  // Nothing can be reached from inside a wall.
  if (walls.some((o) => (o !== s && hits(out, out, o, 2)) || (o !== t && hits(inn, inn, o, 2)))) return null;
  const lines = (first: number, second: number, near: (o: Box) => [number, number]) => {
    const set = new Set<number>([first, second]);
    for (const o of walls) for (const v of near(o)) set.add(v);
    return [...set].sort((x, y) => x - y);
  };
  const xs = lines(out.x, inn.x, (o) => [o.x - CLEAR, o.x + o.w + CLEAR]);
  const ys = lines(out.y, inn.y, (o) => [o.y - CLEAR, o.y + o.h + CLEAR]);
  const W = xs.length;
  const H = ys.length;
  const free = (p: Pt, q: Pt) => walls.every((o) => !hits(p, q, o, CLEAR - 1));
  const STEPS = [DIRS.right, DIRS.left, DIRS.bottom, DIRS.top];
  const dirOf = (d: Pt) => STEPS.findIndex((e) => e.x === d.x && e.y === d.y);
  const key = (i: number, j: number, d: number) => (j * W + i) * 4 + d;
  const size = W * H * 4;
  const dist = new Float64Array(size).fill(Infinity);
  const prev = new Int32Array(size).fill(-1);
  const heap: [number, number][] = [];
  const push = (cost: number, k: number) => {
    heap.push([cost, k]);
    for (let n = heap.length - 1; n > 0; ) {
      const up = (n - 1) >> 1;
      if (heap[up][0] <= heap[n][0]) break;
      [heap[up], heap[n]] = [heap[n], heap[up]];
      n = up;
    }
  };
  const pop = () => {
    const top = heap[0];
    const last = heap.pop()!;
    if (heap.length) {
      heap[0] = last;
      for (let n = 0; ; ) {
        const l = 2 * n + 1;
        const r = l + 1;
        let m = n;
        if (l < heap.length && heap[l][0] < heap[m][0]) m = l;
        if (r < heap.length && heap[r][0] < heap[m][0]) m = r;
        if (m === n) break;
        [heap[m], heap[n]] = [heap[n], heap[m]];
        n = m;
      }
    }
    return top;
  };
  const si = xs.indexOf(out.x);
  const sj = ys.indexOf(out.y);
  const ei = xs.indexOf(inn.x);
  const ej = ys.indexOf(inn.y);
  const startDir = dirOf(DIRS[a]);
  const start = key(si, sj, startDir);
  dist[start] = 0;
  push(0, start);
  const arriveDir = dirOf({ x: -DIRS[b].x, y: -DIRS[b].y });
  let best = -1;
  let bestCost = Infinity;
  while (heap.length) {
    const [cost, k] = pop();
    if (cost > dist[k] || cost >= bestCost) continue;
    const d = k % 4;
    const cell = (k - d) / 4;
    const j = Math.floor(cell / W);
    const i = cell - j * W;
    if (i === ei && j === ej) {
      const total = cost + (d === arriveDir ? 0 : BEND);
      if (d !== dirOf(DIRS[b]) && total < bestCost) {
        bestCost = total;
        best = k;
      }
    }
    for (let nd = 0; nd < 4; nd++) {
      const step = STEPS[nd];
      if (step.x === -STEPS[d].x && step.y === -STEPS[d].y) continue; // no turning back on itself
      const ni = i + step.x;
      const nj = j + step.y;
      if (ni < 0 || nj < 0 || ni >= W || nj >= H) continue;
      const here = { x: xs[i], y: ys[j] };
      const there = { x: xs[ni], y: ys[nj] };
      if (!free(here, there)) continue;
      const next = cost + Math.abs(there.x - here.x) + Math.abs(there.y - here.y) + (nd === d ? 0 : BEND);
      const nk = key(ni, nj, nd);
      if (next < dist[nk]) {
        dist[nk] = next;
        prev[nk] = k;
        push(next, nk);
      }
    }
  }
  if (best < 0) return null;
  const chain: Pt[] = [];
  for (let k = best; k >= 0; k = prev[k]) {
    const cell = (k - (k % 4)) / 4;
    const j = Math.floor(cell / W);
    chain.push({ x: xs[cell - j * W], y: ys[j] });
  }
  chain.reverse();
  return tidy([from, ...chain, to]);
}

/** Line through the corners, rounded; the last run stops `short` before the end. */
export function roundedPath(points: Pt[], short = 0): string {
  const pts = points.slice();
  const n = pts.length;
  if (n < 2) return '';
  if (short > 0) {
    const a = pts[n - 2];
    const b = pts[n - 1];
    const len = Math.abs(b.x - a.x) + Math.abs(b.y - a.y);
    if (len > short) pts[n - 1] = { x: b.x - (Math.sign(b.x - a.x) * short), y: b.y - (Math.sign(b.y - a.y) * short) };
  }
  const f = (v: number) => Math.round(v * 100) / 100;
  let d = `M${f(pts[0].x)} ${f(pts[0].y)}`;
  for (let i = 1; i < n - 1; i++) {
    const p = pts[i - 1];
    const c = pts[i];
    const q = pts[i + 1];
    const toPrev = Math.abs(c.x - p.x) + Math.abs(c.y - p.y);
    const toNext = Math.abs(q.x - c.x) + Math.abs(q.y - c.y);
    const r = Math.min(RADIUS, toPrev / 2, toNext / 2);
    const inX = c.x - Math.sign(c.x - p.x) * r;
    const inY = c.y - Math.sign(c.y - p.y) * r;
    const outX = c.x + Math.sign(q.x - c.x) * r;
    const outY = c.y + Math.sign(q.y - c.y) * r;
    d += ` L${f(inX)} ${f(inY)} Q${f(c.x)} ${f(c.y)} ${f(outX)} ${f(outY)}`;
  }
  return `${d} L${f(pts[n - 1].x)} ${f(pts[n - 1].y)}`;
}

function finish(points: Pt[]): Route {
  const n = points.length;
  const a = points[n - 2];
  const b = points[n - 1];
  let mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
  let longest = -1;
  for (let i = 1; i < n; i++) {
    const p = points[i - 1];
    const q = points[i];
    const len = Math.hypot(q.x - p.x, q.y - p.y);
    if (len > longest) {
      longest = len;
      mid = { x: (p.x + q.x) / 2, y: (p.y + q.y) / 2 };
    }
  }
  return { points, path: roundedPath(points, ARROW - 1), start: points[0], tip: b, angle: (Math.atan2(b.y - a.y, b.x - a.x) * 180) / Math.PI, mid };
}

/** Overlapping tiles leave no way to route: a short line between their edges. */
function overlapping(s: Box, t: Box): Route {
  const edge = (b: Box, to: Pt): Pt => {
    const cx = b.x + b.w / 2;
    const cy = b.y + b.h / 2;
    const dx = to.x - cx;
    const dy = to.y - cy;
    if (dx === 0 && dy === 0) return { x: cx, y: b.y };
    const sx = dx === 0 ? Infinity : b.w / 2 / Math.abs(dx);
    const sy = dy === 0 ? Infinity : b.h / 2 / Math.abs(dy);
    const k = Math.min(sx, sy);
    return { x: cx + dx * k, y: cy + dy * k };
  };
  const sc = { x: s.x + s.w / 2, y: s.y + s.h / 2 };
  const tc = { x: t.x + t.w / 2, y: t.y + t.h / 2 };
  return finish([edge(s, tc), edge(t, sc)]);
}

/**
 * The route from tile s to tile t. `others` are the tiles a wire must not run under;
 * a frame around either end and tiles inside either end are not in the way.
 */
export function routeWire(s: Box, t: Box, others: Box[]): Route {
  const sides = facing(s, t);
  if (!sides) return overlapping(s, t);
  const [a, b] = sides;
  const cx = (s.x + s.w / 2 + t.x + t.w / 2) / 2;
  const cy = (s.y + s.h / 2 + t.y + t.h / 2) / 2;
  const span = Math.abs(s.x - t.x) + Math.abs(s.y - t.y) + NEAR;
  const distance = (o: Box) => Math.abs(o.x + o.w / 2 - cx) + Math.abs(o.y + o.h / 2 - cy);
  const obstacles = others
    .filter((o) => !contains(o, s) && !contains(o, t) && !contains(s, o) && !contains(t, o) && distance(o) < span + o.w + o.h)
    .sort((x, y) => distance(x) - distance(y))
    .slice(0, MAX_OBSTACLES);

  const line = straight(s, t, a, b);
  if (line && clearOf(line, obstacles)) return finish(line);
  const simple = elbow(s, t, a, b);
  if (clearOf(simple, obstacles)) return finish(tidy(simple));

  // In the way: try leaving and arriving on each sensible side, keep the cheapest.
  let best: Pt[] | null = null;
  const pairs: [Side, Side][] = [[a, b]];
  for (const sa of SIDES) for (const sb of SIDES) if (sa !== a || sb !== b) pairs.push([sa, sb]);
  for (const [sa, sb] of pairs) {
    const p = around(s, t, sa, sb, obstacles);
    if (p && (!best || costOf(p) < costOf(best))) best = p;
    // The facing sides almost always win: spare the rest when they found a way.
    if (best && sa === a && sb === b) break;
  }
  return finish(best ?? tidy(simple));
}

/** While a wire is being drawn: the same look, with nothing yet to avoid. */
export function draftRoute(from: Pt, to: Pt, side: Side): Route {
  if (HORIZONTAL(side)) {
    const dir = Math.sign(to.x - from.x) || DIRS[side].x;
    const turn = from.x + dir * Math.min(Math.abs(to.x - from.x) / 2, TRUNK);
    return finish(tidy([from, { x: turn, y: from.y }, { x: turn, y: to.y }, to]));
  }
  const dir = Math.sign(to.y - from.y) || DIRS[side].y;
  const turn = from.y + dir * Math.min(Math.abs(to.y - from.y) / 2, TRUNK);
  return finish(tidy([from, { x: from.x, y: turn }, { x: to.x, y: turn }, to]));
}

