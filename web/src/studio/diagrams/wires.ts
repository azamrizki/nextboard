// Wiring-diagram arrows (docs/API-studio.md §6): every arrow runs in straight horizontal and
// vertical stretches on a grid, around the tiles that are not its own. A search per arrow finds
// the way with few bends and few crossings. Arrows from one source share their way out and split
// at a dot, as wires do; a crossing without a dot is no connection. The hub has the same rules in
// internal/studio/wires.go.

export type Pt = [number, number];

export interface WireBox {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface WireNode {
  box: WireBox;
  shape: string;
}

export interface WireEdge {
  from: number;
  to: number;
  dashed: boolean;
}

export interface Wire {
  /** First point at the source's outline, last point the arrow's tip. */
  route: Pt[];
  /** Where this wire and another from the same source part ways. */
  dots: Pt[];
}

export const WIRE_STEP = 16;
const CLEAR = 16; // what a wire keeps from a tile that is not its own
const MARGIN = 10; // grid lines around the diagram, for a wire with no way between the tiles
const OUTLINE = 5; // a shape's outline lies this far inside its box (drawShape's pad)
const TIP_GAP = 2; // between an arrow's tip and the outline it points at
const PORT_MARGIN = 12;
const MAX_PORT_SPACING = 3; // in grid steps
const PASSES = 2;

// costs, against WIRE_STEP for one step along free grid
const BEND = 48;
const CROSSING = 64;
const SHARED = 4; // running along a wire of the same source
const ALONG = 240; // running along a wire of another source: they could not be told apart
const ON_WIRE = 320; // a corner on another wire looks like a connection
const UNDER_LABEL = 400;
const THROUGH_TILE = 1_000_000; // only when there is no other way at all
const WRONG_WAY = 100_000;

const DX = [1, 0, -1, 0];
const DY = [0, 1, 0, -1];
const horizontal = (dir: number) => dir % 2 === 0;

const snap = (v: number) => Math.round(v / WIRE_STEP) * WIRE_STEP;

/** How far along `dir` the outline of a shape lies at this spot of the side, from the box's centre. */
function outlineAt(n: WireNode, dir: number, coord: number): number {
  const half = (horizontal(dir) ? n.box.w : n.box.h) / 2 - OUTLINE;
  const across = (horizontal(dir) ? n.box.h : n.box.w) / 2 - OUTLINE;
  const centre = horizontal(dir) ? n.box.y + n.box.h / 2 : n.box.x + n.box.w / 2;
  const t = Math.min(1, Math.abs(coord - centre) / Math.max(1, across));
  if (n.shape === 'ellipse') return half * Math.sqrt(1 - t * t);
  if (n.shape === 'diamond') return half * (1 - t);
  return half;
}

/** The width of a side a wire can meet; an ellipse or a diamond falls away towards its ends. */
function usable(n: WireNode, dir: number): number {
  const length = horizontal(dir) ? n.box.h : n.box.w;
  if (n.shape === 'ellipse') return length * 0.6;
  if (n.shape === 'diamond') return length * 0.5;
  return Math.max(0, length - 2 * PORT_MARGIN);
}

/** Which way a wire leaves its source and arrives at its target. */
function sidesOf(a: WireBox, b: WireBox, lr: boolean, self: boolean): [number, number] {
  if (self) return [0, 2];
  const below = b.y >= a.y + a.h;
  const above = b.y + b.h <= a.y;
  const right = b.x >= a.x + a.w;
  const left = b.x + b.w <= a.x;
  if (lr) {
    if (right) return [0, 0];
    if (left) return [2, 2];
    return below ? [1, 1] : [3, 3];
  }
  if (below) return [1, 1];
  if (above) return [3, 3];
  return right ? [0, 0] : [2, 2];
}

interface Port {
  node: number;
  dir: number; // the way out of the tile on this side
  coord: number; // across the side
  sort: number;
  count: number;
}

export function routeWires(nodes: WireNode[], edges: WireEdge[], labels: WireBox[], lr: boolean): Wire[] {
  if (!edges.length) return [];

  // ---- one way out per source and line style, one way in per arrow ----
  const groupIds = new Map<string, number>();
  const groupOf = edges.map((e) => {
    const key = `${e.from}:${e.dashed ? 'd' : 's'}`;
    if (!groupIds.has(key)) groupIds.set(key, groupIds.size);
    return groupIds.get(key)!;
  });
  const sides = edges.map((e) => sidesOf(nodes[e.from].box, nodes[e.to].box, lr, e.from === e.to));
  const centreAcross = (node: number, dir: number) => {
    const b = nodes[node].box;
    return horizontal(dir) ? b.y + b.h / 2 : b.x + b.w / 2;
  };
  const ports = new Map<string, Port>();
  const outPort: Port[] = [];
  const inPort: Port[] = [];
  edges.forEach((e, k) => {
    const [out, into] = sides[k];
    const outKey = `o:${groupOf[k]}:${out}`;
    let p = ports.get(outKey);
    if (!p) {
      p = { node: e.from, dir: out, coord: 0, sort: 0, count: 0 };
      ports.set(outKey, p);
    }
    p.sort += centreAcross(e.to, out);
    p.count++;
    outPort[k] = p;
    const q: Port = { node: e.to, dir: (into + 2) % 4, coord: 0, sort: centreAcross(e.from, into), count: 1 };
    ports.set(`i:${k}`, q);
    inPort[k] = q;
  });
  const bySide = new Map<string, Port[]>();
  for (const p of ports.values()) {
    const key = `${p.node}:${p.dir}`;
    if (!bySide.has(key)) bySide.set(key, []);
    bySide.get(key)!.push(p);
  }
  for (const side of bySide.values()) {
    side.sort((a, b) => a.sort / a.count - b.sort / b.count);
    const n = nodes[side[0].node];
    const dir = side[0].dir;
    const room = usable(n, dir);
    const centre = centreAcross(side[0].node, dir);
    const spacing = side.length > 1 ? Math.max(1, Math.min(MAX_PORT_SPACING, Math.floor(room / (side.length - 1) / WIRE_STEP))) * WIRE_STEP : 0;
    side.forEach((p, i) => {
      const offset = (i - (side.length - 1) / 2) * spacing;
      p.coord = snap(centre + Math.max(-room / 2, Math.min(room / 2, offset)));
    });
  }
  // nothing else on either side and the tiles face each other: a straight wire, not a small jog
  const portsOnSide = (p: Port) => bySide.get(`${p.node}:${p.dir}`)!.length;
  edges.forEach((e, k) => {
    const a = outPort[k];
    const b = inPort[k];
    if (e.from === e.to || a.count !== 1 || portsOnSide(a) !== 1 || portsOnSide(b) !== 1) return;
    const lo = Math.max(centreAcross(a.node, a.dir) - usable(nodes[a.node], a.dir) / 2, centreAcross(b.node, b.dir) - usable(nodes[b.node], b.dir) / 2);
    const hi = Math.min(centreAcross(a.node, a.dir) + usable(nodes[a.node], a.dir) / 2, centreAcross(b.node, b.dir) + usable(nodes[b.node], b.dir) / 2);
    if (hi - lo < WIRE_STEP) return;
    const fits = (v: number) => v >= lo && v <= hi;
    a.coord = b.coord = fits(a.coord) ? a.coord : fits(b.coord) ? b.coord : Math.ceil(lo / WIRE_STEP) * WIRE_STEP;
  });

  // ---- the grid ----
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const { box } of nodes) {
    minX = Math.min(minX, box.x);
    minY = Math.min(minY, box.y);
    maxX = Math.max(maxX, box.x + box.w);
    maxY = Math.max(maxY, box.y + box.h);
  }
  const x0 = Math.floor(minX / WIRE_STEP) - MARGIN;
  const y0 = Math.floor(minY / WIRE_STEP) - MARGIN;
  const W = Math.ceil(maxX / WIRE_STEP) - x0 + MARGIN + 1;
  const H = Math.ceil(maxY / WIRE_STEP) - y0 + MARGIN + 1;
  const cell = (ix: number, iy: number) => iy * W + ix;
  const hard = new Uint8Array(W * H);
  const soft = new Uint8Array(W * H);
  const mark = (into: Uint8Array, b: WireBox, margin: number) => {
    const fromX = Math.max(0, Math.ceil((b.x - margin) / WIRE_STEP) - x0);
    const toX = Math.min(W - 1, Math.floor((b.x + b.w + margin) / WIRE_STEP) - x0);
    const fromY = Math.max(0, Math.ceil((b.y - margin) / WIRE_STEP) - y0);
    const toY = Math.min(H - 1, Math.floor((b.y + b.h + margin) / WIRE_STEP) - y0);
    for (let iy = fromY; iy <= toY; iy++) for (let ix = fromX; ix <= toX; ix++) into[cell(ix, iy)] = 1;
  };
  // a wire may run exactly CLEAR from a tile: the first line free of it is where wires start
  for (const { box } of nodes) mark(hard, box, CLEAR - 1);
  for (const b of labels) mark(soft, b, 4);

  /** The grid crossing just outside a port. */
  const portCell = (p: Port): number => {
    const b = nodes[p.node].box;
    const dir = p.dir;
    let ix: number;
    let iy: number;
    if (horizontal(dir)) {
      ix = (dir === 0 ? Math.ceil((b.x + b.w + CLEAR) / WIRE_STEP) : Math.floor((b.x - CLEAR) / WIRE_STEP)) - x0;
      iy = Math.round(p.coord / WIRE_STEP) - y0;
    } else {
      ix = Math.round(p.coord / WIRE_STEP) - x0;
      iy = (dir === 1 ? Math.ceil((b.y + b.h + CLEAR) / WIRE_STEP) : Math.floor((b.y - CLEAR) / WIRE_STEP)) - y0;
    }
    return cell(Math.max(0, Math.min(W - 1, ix)), Math.max(0, Math.min(H - 1, iy)));
  };
  const starts = outPort.map(portCell);
  const goals = inPort.map(portCell);

  // ---- what already runs where ----
  const hOcc = new Int32Array(W * H);
  const vOcc = new Int32Array(W * H);
  const cellOcc = new Int32Array(W * H);
  const cornerOcc = new Int32Array(W * H);
  const occupy = (arr: Int32Array, i: number, gid: number) => {
    arr[i] = arr[i] === 0 || arr[i] === gid + 1 ? gid + 1 : -1;
  };
  const paths: (number[] | null)[] = edges.map(() => null);
  const fill = (skip: number) => {
    hOcc.fill(0);
    vOcc.fill(0);
    cellOcc.fill(0);
    cornerOcc.fill(0);
    paths.forEach((path, k) => {
      if (!path || k === skip) return;
      const gid = groupOf[k];
      let prevStep = -1;
      for (let i = 0; i < path.length; i++) {
        const c = path[i];
        occupy(cellOcc, c, gid);
        if (i + 1 === path.length) break;
        const d = path[i + 1] - c;
        const isH = Math.abs(d) === 1;
        occupy(isH ? hOcc : vOcc, Math.min(c, path[i + 1]), gid);
        if (prevStep !== -1 && prevStep !== d) occupy(cornerOcc, c, gid);
        prevStep = d;
      }
    });
  };

  // ---- the search ----
  const states = W * H * 4;
  const done = states; // the state after the last step, so that arriving the right way can count
  const dist = new Float64Array(states + 1);
  const prev = new Int32Array(states + 1);
  const heapF: number[] = [];
  const heapS: number[] = [];
  const heapPush = (f: number, s: number) => {
    let i = heapF.length;
    heapF.push(f);
    heapS.push(s);
    while (i > 0) {
      const up = (i - 1) >> 1;
      if (heapF[up] <= f) break;
      heapF[i] = heapF[up];
      heapS[i] = heapS[up];
      i = up;
    }
    heapF[i] = f;
    heapS[i] = s;
  };
  const heapPop = (): number => {
    const top = heapS[0];
    const f = heapF.pop()!;
    const s = heapS.pop()!;
    const n = heapF.length;
    if (n > 0) {
      let i = 0;
      for (;;) {
        let child = 2 * i + 1;
        if (child >= n) break;
        if (child + 1 < n && heapF[child + 1] < heapF[child]) child++;
        if (heapF[child] >= f) break;
        heapF[i] = heapF[child];
        heapS[i] = heapS[child];
        i = child;
      }
      heapF[i] = f;
      heapS[i] = s;
    }
    return top;
  };
  const search = (start: number, startDir: number, goal: number, intoDir: number, gid: number): number[] => {
    dist.fill(Infinity);
    heapF.length = 0;
    heapS.length = 0;
    const gx = goal % W;
    const gy = Math.floor(goal / W);
    const own = gid + 1;
    const first = start * 4 + startDir;
    dist[first] = 0;
    prev[first] = -1;
    heapPush(0, first);
    while (heapF.length) {
      const s = heapPop();
      if (s === done) break;
      const g = dist[s];
      const dir = s % 4;
      const c = (s - dir) / 4;
      if (c === goal) {
        const extra = dir === intoDir ? 0 : (dir + 2) % 4 === intoDir ? WRONG_WAY : BEND;
        if (g + extra < dist[done]) {
          dist[done] = g + extra;
          prev[done] = s;
          heapPush(g + extra, done);
        }
      }
      const ix = c % W;
      const iy = (c - ix) / W;
      for (let nd = 0; nd < 4; nd++) {
        if (nd === (dir + 2) % 4) continue;
        const nx = ix + DX[nd];
        const ny = iy + DY[nd];
        if (nx < 0 || ny < 0 || nx >= W || ny >= H) continue;
        const nc = cell(nx, ny);
        let cost = WIRE_STEP;
        if (nd !== dir) {
          cost += BEND;
          if (cellOcc[c] !== 0 && cellOcc[c] !== own) cost += ON_WIRE;
        }
        const on = horizontal(nd) ? hOcc[Math.min(c, nc)] : vOcc[Math.min(c, nc)];
        if (on === own) cost = nd === dir ? SHARED : SHARED + BEND;
        else if (on !== 0) cost += ALONG;
        else if (cellOcc[nc] !== 0 && cellOcc[nc] !== own) cost += CROSSING;
        if (cornerOcc[nc] !== 0 && cornerOcc[nc] !== own) cost += ON_WIRE;
        if (hard[nc]) cost += THROUGH_TILE;
        if (soft[nc]) cost += UNDER_LABEL;
        const ns = nc * 4 + nd;
        const g2 = g + cost;
        if (g2 < dist[ns]) {
          dist[ns] = g2;
          prev[ns] = s;
          heapPush(g2 + (Math.abs(nx - gx) + Math.abs(ny - gy)) * WIRE_STEP, ns);
        }
      }
    }
    const out: number[] = [];
    let s = prev[done];
    while (s !== -1 && s !== undefined) {
      const c = (s - s % 4) / 4;
      if (out[out.length - 1] !== c) out.push(c);
      s = prev[s];
    }
    return out.reverse();
  };

  // short wires first: the long ones go around them; then each again, knowing all the others
  const span = (k: number) => {
    const a = starts[k];
    const b = goals[k];
    return Math.abs(a % W - b % W) + Math.abs(Math.floor(a / W) - Math.floor(b / W));
  };
  const order = edges.map((_, k) => k).sort((a, b) => span(a) - span(b) || a - b);
  for (let pass = 0; pass < PASSES; pass++) {
    for (const k of order) {
      fill(k);
      prev[done] = -1;
      paths[k] = search(starts[k], outPort[k].dir, goals[k], (inPort[k].dir + 2) % 4, groupOf[k]);
    }
  }

  // ---- back to board coordinates ----
  const point = (c: number): Pt => [(c % W + x0) * WIRE_STEP, (Math.floor(c / W) + y0) * WIRE_STEP];
  const portPoint = (p: Port, inset: number): Pt => {
    const b = nodes[p.node].box;
    const depth = outlineAt(nodes[p.node], p.dir, p.coord) + inset;
    if (horizontal(p.dir)) return [b.x + b.w / 2 + DX[p.dir] * depth, p.coord];
    return [p.coord, b.y + b.h / 2 + DY[p.dir] * depth];
  };
  const wires: Wire[] = edges.map((_, k) => {
    const path = paths[k]!;
    const route = cleanRoute([portPoint(outPort[k], -1), ...path.map(point), portPoint(inPort[k], TIP_GAP)]);
    return { route, dots: [] };
  });

  // a dot where three ways meet, on the first wire of the group that passes it
  const groupCells = new Map<number, Map<number, number>>();
  paths.forEach((path, k) => {
    const cells = groupCells.get(groupOf[k]) ?? new Map<number, number>();
    groupCells.set(groupOf[k], cells);
    path!.forEach((c, i) => {
      let bits = cells.get(c) ?? 0;
      if (i === 0) bits |= 1 << ((outPort[k].dir + 2) % 4);
      if (i + 1 < path!.length) {
        const d = path![i + 1] - c;
        bits |= 1 << (d === 1 ? 0 : d === W ? 1 : d === -1 ? 2 : 3);
      }
      if (i > 0) {
        const d = path![i - 1] - c;
        bits |= 1 << (d === 1 ? 0 : d === W ? 1 : d === -1 ? 2 : 3);
      }
      cells.set(c, bits);
    });
  });
  for (const [gid, cells] of groupCells) {
    for (const [c, bits] of cells) {
      if ((bits & 1) + ((bits >> 1) & 1) + ((bits >> 2) & 1) + ((bits >> 3) & 1) < 3) continue;
      const k = edges.findIndex((_, i) => groupOf[i] === gid && paths[i]!.includes(c));
      if (k >= 0) wires[k].dots.push(point(c));
    }
  }
  return wires;
}

/** Without repeated points and the points in the middle of a straight stretch. */
export function cleanRoute(pts: Pt[]): Pt[] {
  const out: Pt[] = [];
  for (const p of pts) {
    const last = out[out.length - 1];
    if (last && last[0] === p[0] && last[1] === p[1]) continue;
    const before = out[out.length - 2];
    if (before && ((before[0] === last[0] && last[0] === p[0]) || (before[1] === last[1] && last[1] === p[1]))) out.pop();
    out.push(p);
  }
  return out;
}
