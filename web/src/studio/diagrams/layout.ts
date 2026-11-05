// nb.diagram/v1 → loose board shapes (docs/API-studio.md §6). Layered layout like the
// hub's own landing (internal/studio/layout.go: longest-path layers with cycle breaking,
// barycenter ordering, top-to-bottom or left-to-right), so a diagram looks the same
// whether the web client or the hub lands it. No layout library is needed (elkjs is
// EPL-2.0, open issue #13). Every node carries data.diagramId + data.diagramKey.
//
// What goes on the board must stay readable: a frame is sized from its wrapped text, an
// arrow bends around tiles that are not its own, and a label sits beside its arrow where
// nothing else is. The sizes here follow the styles of .shape-label and .text-body.

import { newId } from '../../lib/ids';
import type { BoardNode } from '../../lib/types';
import { DIAGRAM_GROUP_COLORS, DIAGRAM_WIRE_COLORS, INK_COLORS } from '../../board/palette';
import type { ArrowColor, ArrowStyle, Diagram, DiagramStyle } from '../types';
import { routeWires, WIRE_STEP } from './wires';

export const INK = INK_COLORS[0].value;
const GROUP_COLORS = DIAGRAM_GROUP_COLORS;

const GAP_MAIN = 100;
const GAP_CROSS = 64;
const ZONE_PAD = 24;
const ZONE_HEAD = 18;
const TITLE_H = 68;
const TITLE_BOX_H = 56; // .font-hand makes "l" 35 px, at line-height 1.3 + padding
const NODE_MIN_W = 130;
const NODE_MIN_H = 64;
const LINE_H = 31.25; // .font-hand makes "m" 25 px, at line-height 1.25
const RECT_PAD = 16; // .shape-label inset 12 + room for a rough outline
const RECT_TEXT_MAX = 300;
const ELLIPSE_TEXT_MAX = 250;
const DIAMOND_TEXT_MAX = [140, 190, 240, 300, 360];
const LABEL_H = 32; // .font-hand makes "s" 17.5 px, at line-height 1.3 + padding
const LABEL_SCALE = 0.72; // a label's 17.5 px against a node's 25 px
const CLEARANCE = 12; // what an arrow keeps from a tile that is not its own
const ARROW_GAP = 6; // between an arrow's tip and the outline it points at
const BEND_STEP = 36;
const BEND_TRIES = 60;
const PAIR_BEND = 30;
const SAMPLES = 24;
const LABEL_DISTANCES = [8, 30, 56, 84];
/** Moves the top of a bend along the arrow, for one that has to clear something near one of its ends. */
const BEND_ALONG = [0, -0.3, 0.3];

/** How the arrows look. No arrowColor: they follow the shapes, as before there was a choice. */
export interface DiagramLook {
  arrows?: ArrowStyle;
  arrowColor?: ArrowColor;
}

const WIRE_LABEL_DISTANCES = [6, 14, 24, 40, 64, 96, 140, 200]; // the far ones only where nothing nearer is free
const WIRE_LABEL_SPOTS = [0.5, 0.35, 0.65, 0.2, 0.8, 0.1, 0.9];
const WIRE_LABEL_NEAR = 24; // a free spot this close ends the search; a further one has to beat every other stretch
const MAX_TRACKS = 14; // the room a gap between layers makes for wires; more find their way around

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

type Pt = [number, number];

/** Width of one character of the hand font at 25 px, measured and on the generous side. */
function charW(c: string): number {
  const code = c.codePointAt(0) ?? 0;
  if (code > 0x2e7f) return 25; // wide scripts
  if (' il.,:;\'!'.includes(c)) return 6.5;
  if ('mwMW@%&#'.includes(c)) return 16;
  if (c >= 'A' && c <= 'Z') return 13.5;
  return 11;
}

const textW = (s: string) => [...s].reduce((sum, c) => sum + charW(c), 0);

/** Lines of `text` in `limit` px: words stay whole unless one alone is too long, as in the labels' CSS. */
export function wrapText(text: string, limit: number): string[] {
  const lines: string[] = [];
  for (const para of text.split('\n')) {
    let cur = '';
    let curW = 0;
    for (const word of para.split(' ')) {
      const w = textW(word);
      const space = cur ? charW(' ') : 0;
      if (curW + space + w <= limit) {
        cur += (cur ? ' ' : '') + word;
        curW += space + w;
        continue;
      }
      if (cur) lines.push(cur);
      cur = '';
      curW = 0;
      for (const c of word) {
        if (curW + charW(c) > limit) {
          lines.push(cur);
          cur = '';
          curW = 0;
        }
        cur += c;
        curW += charW(c);
      }
    }
    lines.push(cur);
  }
  return lines;
}

function nodeLines(n: Diagram['nodes'][number], limit: number): string[] {
  return [...wrapText(n.label, limit), ...(n.note ? wrapText(n.note, limit) : [])];
}

/** The frame for a node's text: the text sits in a rectangle inside the outline (.shape-label). */
function nodeSize(n: Diagram['nodes'][number]): { w: number; h: number } {
  const fit = (lines: string[]) => ({ tw: Math.max(0, ...lines.map(textW)), th: lines.length * LINE_H });
  let w: number;
  let h: number;
  if (n.shape === 'diamond') {
    // half of each side is usable; wider rather than tall, up to a point
    let lines = nodeLines(n, DIAMOND_TEXT_MAX[0]);
    for (const limit of DIAMOND_TEXT_MAX) {
      lines = nodeLines(n, limit);
      if (lines.length <= 3) break;
    }
    const { tw, th } = fit(lines);
    w = Math.max(170, 2 * tw + 16);
    h = Math.max(100, 2 * th + 16);
  } else if (n.shape === 'ellipse') {
    const { tw, th } = fit(nodeLines(n, ELLIPSE_TEXT_MAX));
    w = Math.max(NODE_MIN_W, (tw + 8) / 0.7);
    h = Math.max(NODE_MIN_H, (th + 8) / 0.7);
  } else {
    const { tw, th } = fit(nodeLines(n, RECT_TEXT_MAX));
    w = Math.max(NODE_MIN_W, tw + 2 * RECT_PAD);
    h = Math.max(NODE_MIN_H, th + 2 * RECT_PAD);
  }
  return { w: Math.round(w), h: Math.round(h) };
}

const labelW = (text: string) => Math.max(40, Math.ceil(textW(text) * LABEL_SCALE) + 16);

function layers(d: Diagram): number[][] {
  const idx = new Map(d.nodes.map((n, i) => [n.id, i]));
  const n = d.nodes.length;
  const out: number[][] = Array.from({ length: n }, () => []);
  const inn: number[][] = Array.from({ length: n }, () => []);
  const succ: number[][] = Array.from({ length: n }, () => []);
  for (const e of d.edges) {
    const a = idx.get(e.from);
    const b = idx.get(e.to);
    if (a !== undefined && b !== undefined && a !== b) succ[a].push(b);
  }
  // drop back edges (cycle breaking)
  const state = new Array<number>(n).fill(0);
  const dfs = (v: number) => {
    state[v] = 1;
    for (const w of succ[v]) {
      if (state[w] === 0) {
        out[v].push(w);
        inn[w].push(v);
        dfs(w);
      } else if (state[w] === 2) {
        out[v].push(w);
        inn[w].push(v);
      }
    }
    state[v] = 2;
  };
  for (let v = 0; v < n; v++) if (state[v] === 0) dfs(v);
  const memo = new Map<number, number>();
  const depth = (v: number): number => {
    const m = memo.get(v);
    if (m !== undefined) return m;
    let best = 0;
    for (const p of inn[v]) best = Math.max(best, depth(p) + 1);
    memo.set(v, best);
    return best;
  };
  let maxL = 0;
  const layer = d.nodes.map((_, v) => {
    const l = depth(v);
    maxL = Math.max(maxL, l);
    return l;
  });
  const ls: number[][] = Array.from({ length: maxL + 1 }, () => []);
  layer.forEach((l, v) => ls[l].push(v));
  const pos = new Array<number>(n).fill(0);
  ls.forEach((l) => l.forEach((v, i) => (pos[v] = i)));
  const sweep = (l: number[], nb: (v: number) => number[]) => {
    const bc = new Map<number, number>();
    for (const v of l) {
      const ns = nb(v);
      bc.set(v, ns.length ? ns.reduce((s, w) => s + pos[w], 0) / ns.length : pos[v]);
    }
    l.sort((a, b) => bc.get(a)! - bc.get(b)!);
    l.forEach((v, i) => (pos[v] = i));
  };
  for (let it = 0; it < 3; it++) {
    for (let k = 1; k < ls.length; k++) sweep(ls[k], (v) => inn[v]);
    for (let k = ls.length - 2; k >= 0; k--) sweep(ls[k], (v) => out[v]);
  }
  return ls;
}

/** Where the ray from a node's centre towards `to` leaves its outline, plus `gap`. */
function border(b: Box, shape: string, to: Pt, gap: number): Pt {
  const cx = b.x + b.w / 2;
  const cy = b.y + b.h / 2;
  const dx = to[0] - cx;
  const dy = to[1] - cy;
  const dist = Math.hypot(dx, dy);
  if (dist === 0) return [cx, cy];
  const rx = dx / (b.w / 2);
  const ry = dy / (b.h / 2);
  let s: number;
  if (shape === 'diamond') s = 1 / (Math.abs(rx) + Math.abs(ry));
  else if (shape === 'ellipse') s = 1 / Math.hypot(rx, ry);
  else s = 1 / Math.max(Math.abs(rx), Math.abs(ry));
  return [cx + dx * s + (dx / dist) * gap, cy + dy * s + (dy / dist) * gap];
}

/** The curve a line tile draws: from p0 to p2, through mid halfway. */
function curveAt(p0: Pt, mid: Pt, p2: Pt, t: number): Pt {
  const u = 1 - t;
  const c: Pt = [2 * mid[0] - (p0[0] + p2[0]) / 2, 2 * mid[1] - (p0[1] + p2[1]) / 2];
  return [u * u * p0[0] + 2 * u * t * c[0] + t * t * p2[0], u * u * p0[1] + 2 * u * t * c[1] + t * t * p2[1]];
}

function curveSamples(p0: Pt, mid: Pt, p2: Pt): Pt[] {
  const out: Pt[] = [];
  for (let i = 0; i <= SAMPLES; i++) out.push(curveAt(p0, mid, p2, i / SAMPLES));
  return out;
}

const overlaps = (a: Box, b: Box) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

/** Does a line through `pts` touch a box grown by `margin`? Every segment, so a long curve can't step over it. */
function touches(pts: Pt[], b: Box, margin: number): boolean {
  const x0 = b.x - margin;
  const y0 = b.y - margin;
  const x1 = b.x + b.w + margin;
  const y1 = b.y + b.h + margin;
  for (let i = 1; i < pts.length; i++) {
    const [ax, ay] = pts[i - 1];
    const dx = pts[i][0] - ax;
    const dy = pts[i][1] - ay;
    let t0 = 0;
    let t1 = 1;
    let inside = true;
    for (const [p, q] of [[-dx, ax - x0], [dx, x1 - ax], [-dy, ay - y0], [dy, y1 - ay]]) {
      if (p === 0) {
        if (q < 0) inside = false;
      } else if (p < 0) {
        t0 = Math.max(t0, q / p);
      } else {
        t1 = Math.min(t1, q / p);
      }
    }
    if (inside && t0 <= t1) return true;
  }
  return false;
}

/** The share of a curve's middle that runs along a curve already drawn. */
function runsAlong(pts: Pt[], drawn: Pt[][]): boolean {
  let close = 0;
  let total = 0;
  for (let i = 5; i <= SAMPLES - 5; i += 2) {
    total++;
    if (drawn.some((d) => d.some((q) => Math.hypot(q[0] - pts[i][0], q[1] - pts[i][1]) < 9))) close++;
  }
  return close / total > 0.35;
}

/** Does a curve run through a tile it starts or ends at? Both ends lie just outside its outline. */
function crossesOwn(from: Pt, mid: Pt, to: Pt, b: Box, shape: string): boolean {
  const hw = b.w / 2;
  const hh = b.h / 2;
  for (let i = 1; i < 2 * SAMPLES; i++) {
    const p = curveAt(from, mid, to, i / (2 * SAMPLES));
    const dx = Math.abs(p[0] - (b.x + hw)) / hw;
    const dy = Math.abs(p[1] - (b.y + hh)) / hh;
    if (shape === 'diamond' ? dx + dy < 1 : shape === 'ellipse' ? dx * dx + dy * dy < 1 : dx < 1 && dy < 1) return true;
  }
  return false;
}

export interface Laid {
  nodes: { id: string; box: Box; shape: string; label: string; note: string; color: string; lines: { label: string[]; note: string[] } }[];
  edges: { key: string; from: Pt; mid: Pt; to: Pt; route: Pt[] | null; dots: Pt[]; dashed: boolean; color: string; label: string; labelBox: Box | null; labelAlign: 'left' | 'center' | 'right' }[];
  zones: { id: string; box: Box; label: string; color: string; labelBox: Box }[];
  title: { text: string; box: Box } | null;
  width: number;
  height: number;
}

interface Route {
  from: Pt;
  mid: Pt;
  to: Pt;
  pts: Pt[];
  /** Touches nothing it should not. */
  clean: boolean;
}

/** Bends an edge as little as it takes to pass no tile that is not its own; reach 0 tries only firstBend. */
function routeEdge(a: Box, aShape: string, b: Box, bShape: string, firstBend: number, others: Box[], drawn: Pt[][], reach: number): Route {
  const ca: Pt = [a.x + a.w / 2, a.y + a.h / 2];
  const cb: Pt = [b.x + b.w / 2, b.y + b.h / 2];
  const dx = cb[0] - ca[0];
  const dy = cb[1] - ca[1];
  const dist = Math.hypot(dx, dy) || 1;
  const nx = -dy / dist;
  const ny = dx / dist;
  const middle: Pt = [(ca[0] + cb[0]) / 2, (ca[1] + cb[1]) / 2];
  const tries: [number, number][] = [[firstBend, 0]];
  for (let k = 1; k <= BEND_TRIES && k * BEND_STEP <= reach; k++) for (const along of BEND_ALONG) tries.push([firstBend + k * BEND_STEP, along], [firstBend - k * BEND_STEP, along]);
  let best: Route | null = null;
  let bestScore = Infinity;
  for (const [o, along] of tries) {
    const apex: Pt = [middle[0] + nx * o + dx * along, middle[1] + ny * o + dy * along];
    const from = border(a, aShape, apex, 2);
    const to = border(b, bShape, apex, ARROW_GAP);
    const mid: Pt = o === 0 ? [(from[0] + to[0]) / 2, (from[1] + to[1]) / 2] : apex;
    const pts = curveSamples(from, mid, to);
    let hits = 0;
    for (const box of others) if (touches(pts, box, CLEARANCE)) hits++;
    // worse than passing another tile: the arrow seems to come out on the wrong side
    if (crossesOwn(from, mid, to, a, aShape) || crossesOwn(from, mid, to, b, bShape)) hits += 3;
    const score = hits * 1000 + (runsAlong(pts, drawn) ? 500 : 0) + Math.abs(o) / 100;
    if (score < bestScore) {
      bestScore = score;
      best = { from, mid, to, pts, clean: hits === 0 && score < 500 };
    }
    if (hits === 0 && score < 500) break;
  }
  return best!;
}

/** A spot beside the curve where the label hides nothing and nothing hides it. */
function placeLabel(pts: Pt[], from: Pt, mid: Pt, to: Pt, w: number, boxes: Box[], lines: Pt[][]): { box: Box; align: 'left' | 'center' | 'right' } {
  let best: { box: Box; align: 'left' | 'center' | 'right' } | null = null;
  let bestScore = Infinity;
  const spots = [0.5, 0.4, 0.6, 0.3, 0.7, 0.2, 0.8];
  // close to its arrow first, further out only when there is no room
  for (const far of LABEL_DISTANCES) {
    for (let s = 0; s < spots.length; s++) {
      const p = curveAt(from, mid, to, spots[s]);
      const before = curveAt(from, mid, to, spots[s] - 0.02);
      const after = curveAt(from, mid, to, spots[s] + 0.02);
      const tx = after[0] - before[0];
      const ty = after[1] - before[1];
      const right = { box: { x: p[0] + far, y: p[1] - LABEL_H / 2, w, h: LABEL_H }, align: 'left' as const };
      const left = { box: { x: p[0] - far - w, y: p[1] - LABEL_H / 2, w, h: LABEL_H }, align: 'right' as const };
      const above = { box: { x: p[0] - w / 2, y: p[1] - LABEL_H - far + 6, w, h: LABEL_H }, align: 'center' as const };
      const below = { box: { x: p[0] - w / 2, y: p[1] + far - 6, w, h: LABEL_H }, align: 'center' as const };
      const sides = Math.abs(tx) < Math.abs(ty) * 0.6 ? [right, left, above, below] : Math.abs(ty) < Math.abs(tx) * 0.6 ? [above, below, right, left] : tx * ty > 0 ? [{ ...right, box: { ...right.box, y: p[1] - LABEL_H } }, { ...left, box: { ...left.box, y: p[1] } }, above, below] : [{ ...right, box: { ...right.box, y: p[1] } }, { ...left, box: { ...left.box, y: p[1] - LABEL_H } }, above, below];
      for (const c of sides) {
        let score = s + far / 10;
        for (const b of boxes) if (overlaps(c.box, { x: b.x - 4, y: b.y - 4, w: b.w + 8, h: b.h + 8 })) score += 1000;
        for (const l of lines) if (touches(l, c.box, 4)) score += l === pts ? 300 : 100;
        if (score < bestScore) {
          bestScore = score;
          best = c;
        }
        if (score < 100) return c;
      }
    }
  }
  return best!;
}

/** A spot beside a wire: on a stretch of its own if it has one, where the label hides nothing. */
function placeWireLabel(route: Pt[], w: number, boxes: Box[], lines: Pt[][], own: number): { box: Box; align: 'left' | 'center' | 'right' } {
  const stretches = route.slice(1).map((p, i) => {
    const a = route[i];
    const length = Math.abs(p[0] - a[0]) + Math.abs(p[1] - a[1]);
    const mid: Box = { x: (a[0] + p[0]) / 2, y: (a[1] + p[1]) / 2, w: 0, h: 0 };
    const shared = lines.some((l, k) => k !== own && touches(l, mid, 1));
    return { a, b: p, length, rank: (shared ? 30 : 0) - Math.min(length, 400) / 20 };
  });
  stretches.sort((x, y) => x.rank - y.rank);
  let best: { box: Box; align: 'left' | 'center' | 'right' } | null = null;
  let bestScore = Infinity;
  let settled = false;
  for (const st of stretches) {
    const upright = st.a[0] === st.b[0];
    for (const far of WIRE_LABEL_DISTANCES) {
      for (const t of WIRE_LABEL_SPOTS) {
        const x = st.a[0] + (st.b[0] - st.a[0]) * t;
        const y = st.a[1] + (st.b[1] - st.a[1]) * t;
        const spots = upright
          ? [
              { box: { x: x + far, y: y - LABEL_H / 2, w, h: LABEL_H }, align: 'left' as const },
              { box: { x: x - far - w, y: y - LABEL_H / 2, w, h: LABEL_H }, align: 'right' as const },
            ]
          : [
              { box: { x: x - w / 2, y: y - LABEL_H - far + 6, w, h: LABEL_H }, align: 'center' as const },
              { box: { x: x - w / 2, y: y + far - 6, w, h: LABEL_H }, align: 'center' as const },
            ];
        for (const c of spots) {
          let hits = 0;
          for (const b of boxes) if (overlaps(c.box, { x: b.x - 4, y: b.y - 4, w: b.w + 8, h: b.h + 8 })) hits += 1000;
          lines.forEach((l, k) => {
            if (touches(l, c.box, 3)) hits += k === own ? 300 : 100;
          });
          const score = hits + st.rank + far + Math.abs(t - 0.5) * 4;
          if (score < bestScore) {
            bestScore = score;
            best = c;
            settled = hits === 0 && far <= WIRE_LABEL_NEAR;
          }
        }
      }
      if (settled) return best!;
    }
  }
  return best!;
}

/** Wires run straighter between tiles that face each other: each layer moves towards what it connects to, keeping its order. */
function settleForWires(d: Diagram, ls: number[][], size: Box[], lr: boolean, crossGap: number[], idx: Map<string, number>, layerOf: Map<number, number>) {
  const half = (v: number) => (lr ? size[v].h : size[v].w) / 2;
  const centre = (v: number) => (lr ? size[v].y : size[v].x) + half(v);
  const setCentre = (v: number, c: number) => {
    if (lr) size[v].y = c - half(v);
    else size[v].x = c - half(v);
  };
  const near: number[][] = d.nodes.map(() => []);
  for (const e of d.edges) {
    const a = idx.get(e.from);
    const b = idx.get(e.to);
    if (a === undefined || b === undefined || layerOf.get(a) === layerOf.get(b)) continue;
    near[a].push(b);
    near[b].push(a);
  }
  const place = (k: number, want: number[]) => {
    const l = ls[k];
    const push = want.slice();
    for (let i = 1; i < l.length; i++) push[i] = Math.max(push[i], push[i - 1] + half(l[i - 1]) + crossGap[k] + half(l[i]));
    const pull = want.slice();
    for (let i = l.length - 2; i >= 0; i--) pull[i] = Math.min(pull[i], pull[i + 1] - half(l[i + 1]) - crossGap[k] - half(l[i]));
    l.forEach((v, i) => setCentre(v, (push[i] + pull[i]) / 2));
  };
  for (let it = 0; it < 8; it++) {
    const down = it % 2 === 0;
    const order = ls.map((_, k) => k);
    if (!down) order.reverse();
    for (const k of order) {
      place(
        k,
        ls[k].map((v) => {
          const from = near[v].filter((o) => (down ? layerOf.get(o)! < k : layerOf.get(o)! > k));
          return from.length ? from.reduce((sum, o) => sum + centre(o), 0) / from.length : centre(v);
        }),
      );
    }
  }
  // the middle of every tile on a grid line, so that a wire can meet it there
  ls.forEach((l, k) => {
    let min = -Infinity;
    l.forEach((v, i) => {
      const c = Math.max(Math.round(centre(v) / WIRE_STEP) * WIRE_STEP, min);
      setCentre(v, c);
      if (i + 1 < l.length) min = Math.ceil((c + half(v) + crossGap[k] + half(l[i + 1])) / WIRE_STEP) * WIRE_STEP;
    });
    for (const v of l) {
      if (lr) size[v].x = Math.round((size[v].x + size[v].w / 2) / WIRE_STEP) * WIRE_STEP - size[v].w / 2;
      else size[v].y = Math.round((size[v].y + size[v].h / 2) / WIRE_STEP) * WIRE_STEP - size[v].h / 2;
    }
  });
}

let lastLaid: { d: Diagram; style: DiagramStyle; arrows?: ArrowStyle; arrowColor?: ArrowColor; laid: Laid } | null = null;

/** Positions of everything, top left at (0, 0). A landing asks for its size and then its nodes: the same work once. */
export function layoutDiagram(d: Diagram, style: DiagramStyle, look: DiagramLook = {}): Laid {
  const l = lastLaid;
  if (l && l.d === d && l.style === style && l.arrows === look.arrows && l.arrowColor === look.arrowColor) return l.laid;
  const laid = computeLayout(d, style, look);
  lastLaid = { d, style, arrows: look.arrows, arrowColor: look.arrowColor, laid };
  return laid;
}

function computeLayout(d: Diagram, style: DiagramStyle, look: DiagramLook): Laid {
  const wired = look.arrows === 'wiring';
  const ls = layers(d);
  const size: Box[] = d.nodes.map((nd) => ({ x: 0, y: 0, ...nodeSize(nd) }));
  // even, so that the middle of a tile can lie exactly on a grid line
  if (wired) {
    for (const b of size) {
      b.w += b.w % 2;
      b.h += b.h % 2;
    }
  }
  const lr = d.direction === 'LR';
  const idx = new Map(d.nodes.map((n, i) => [n.id, i]));
  const layerOf = new Map<number, number>();
  ls.forEach((l, k) => l.forEach((v) => layerOf.set(v, k)));
  const crossGap = ls.map(() => GAP_CROSS);
  const extent = (l: number[], k: number) => {
    let main = 0;
    let cross = 0;
    l.forEach((v, i) => {
      const mw = lr ? size[v].w : size[v].h;
      const cw = lr ? size[v].h : size[v].w;
      main = Math.max(main, mw);
      cross += cw + (i > 0 ? crossGap[k] : 0);
    });
    return { main, cross };
  };
  // left to right, a label runs along its arrow: the gap has to hold it
  const gaps = ls.map(() => GAP_MAIN);
  if (lr) {
    for (const e of d.edges) {
      const a = layerOf.get(idx.get(e.from) ?? -1);
      const b = layerOf.get(idx.get(e.to) ?? -1);
      if (e.label && a !== undefined && b !== undefined && Math.abs(a - b) === 1) gaps[Math.min(a, b)] = Math.max(gaps[Math.min(a, b)], labelW(e.label) + 40);
    }
  }
  // wires lie side by side: the gaps have to hold them
  if (wired) {
    const tracks = ls.map(() => new Set<string>());
    const passing = ls.map(() => 0);
    const labelled = ls.map(() => false);
    for (const e of d.edges) {
      const a = layerOf.get(idx.get(e.from) ?? -1);
      const b = layerOf.get(idx.get(e.to) ?? -1);
      if (a === undefined || b === undefined) continue;
      for (let k = Math.min(a, b); k < Math.max(a, b); k++) {
        tracks[k].add(`${e.from}:${e.style === 'dashed'}`); // wires of one source share a track
        if (e.label) labelled[k] = true;
        if (k > Math.min(a, b)) passing[k]++;
      }
    }
    ls.forEach((l, k) => {
      gaps[k] = Math.max(gaps[k], WIRE_STEP * (Math.min(tracks[k].size, MAX_TRACKS) + 3) + (labelled[k] && !lr ? LABEL_H : 0));
      crossGap[k] = Math.max(GAP_CROSS, WIRE_STEP * (Math.ceil(passing[k] / (l.length + 1)) + 3));
    });
  }
  const widest = Math.max(0, ...ls.map((l, k) => extent(l, k).cross));
  let m = 0;
  ls.forEach((l, k) => {
    const { main, cross } = extent(l, k);
    let c = (widest - cross) / 2;
    for (const v of l) {
      const b = size[v];
      if (lr) {
        b.x = m + (main - b.w) / 2;
        b.y = c;
        c += b.h + crossGap[k];
      } else {
        b.x = c;
        b.y = m + (main - b.h) / 2;
        c += b.w + crossGap[k];
      }
      b.x = Math.round(b.x);
      b.y = Math.round(b.y);
    }
    m += main + gaps[k];
  });
  if (wired) settleForWires(d, ls, size, lr, crossGap, idx, layerOf);
  const gcolor = new Map(d.groups.map((g, i) => [g.id, GROUP_COLORS[i % GROUP_COLORS.length]]));
  const stroke = (group?: string) => (style === 'color' && group && gcolor.get(group)) || INK;
  // colourful arrows: wires from one source alike, so that one can be followed across the diagram
  const sourceColor = new Map<string, string>();
  for (const v of ls.flat()) {
    const id = d.nodes[v].id;
    if (d.edges.some((e) => e.from === id)) sourceColor.set(id, DIAGRAM_WIRE_COLORS[sourceColor.size % DIAGRAM_WIRE_COLORS.length]);
  }
  const arrowColor = (from: string) => {
    if (look.arrowColor === 'mono') return INK;
    const group = d.nodes[idx.get(from) ?? 0]?.group;
    if (look.arrowColor === 'color') return (style === 'color' && group && gcolor.get(group)) || sourceColor.get(from) || INK;
    return stroke(group);
  };

  const zones: Laid['zones'] = [];
  if (d.groups.length >= 2) {
    const boxes = new Map<string, Box>();
    for (const g of d.groups) {
      let z: Box | null = null;
      d.nodes.forEach((nd, i) => {
        if (nd.group !== g.id) return;
        const b = size[i];
        if (!z) z = { ...b };
        else {
          const x2 = Math.max(z.x + z.w, b.x + b.w);
          const y2 = Math.max(z.y + z.h, b.y + b.h);
          z.x = Math.min(z.x, b.x);
          z.y = Math.min(z.y, b.y);
          z.w = x2 - z.x;
          z.h = y2 - z.y;
        }
      });
      const zz = z as Box | null;
      if (zz) boxes.set(g.id, { x: zz.x - ZONE_PAD, y: zz.y - ZONE_PAD - ZONE_HEAD, w: zz.w + 2 * ZONE_PAD, h: zz.h + 2 * ZONE_PAD + ZONE_HEAD });
    }
    const clash = new Set<string>();
    for (const [a, za] of boxes) {
      for (const [b, zb] of boxes) if (a < b && overlaps(za, zb)) clash.add(a).add(b);
      d.nodes.forEach((nd, i) => {
        if (nd.group !== a && overlaps(za, size[i])) clash.add(a);
      });
    }
    for (const g of d.groups) {
      const z = boxes.get(g.id);
      if (z && !clash.has(g.id)) zones.push({ id: g.id, box: z, label: g.label, color: stroke(g.id), labelBox: { x: z.x + 10, y: z.y + 4, w: z.w - 20, h: LABEL_H } });
    }
  }

  // First the arrows that can run as they are, with their labels; the others then bend
  // around those labels as well as around the tiles.
  const reach = 2 * Math.max(widest, m) + 2 * BEND_STEP;
  const seen = new Map<string, number>();
  const bends = d.edges.map((e) => {
    // more than one arrow between two tiles: side by side. The way back bends the other way by itself.
    const pair = `${e.from}>${e.to}`;
    const nth = seen.get(pair) ?? 0;
    seen.set(pair, nth + 1);
    return nth > 0 || d.edges.some((o) => o.from === e.to && o.to === e.from) ? PAIR_BEND * (nth + 1) : 0;
  });
  const drawn: Pt[][] = [];
  const taken: Box[] = [...size, ...zones.map((z) => z.labelBox)];
  const routes: (Route | null)[] = d.edges.map(() => null);
  const spots: ({ box: Box; align: 'left' | 'center' | 'right' } | null)[] = d.edges.map(() => null);
  const route = (k: number, far: number) => {
    const e = d.edges[k];
    const ai = idx.get(e.from) ?? 0;
    const bi = idx.get(e.to) ?? 0;
    const a = size[ai];
    const b = size[bi];
    if (e.from === e.to) {
      const from: Pt = [a.x + a.w, a.y + a.h / 2 - 10];
      const to: Pt = [a.x + a.w, a.y + a.h / 2 + 10];
      const mid: Pt = [a.x + a.w + 40, a.y + a.h / 2];
      return { from, mid, to, pts: curveSamples(from, mid, to), clean: true };
    }
    return routeEdge(a, d.nodes[ai].shape, b, d.nodes[bi].shape, bends[k], taken.filter((t) => t !== a && t !== b), drawn, far);
  };
  const label = (k: number) => {
    const e = d.edges[k];
    const r = routes[k];
    if (!e.label || !r) return;
    const spot = placeLabel(r.pts, r.from, r.mid, r.to, labelW(e.label), taken, drawn);
    spots[k] = spot;
    taken.push(spot.box);
  };
  const dots: Pt[][] = d.edges.map(() => []);
  if (wired) {
    const wireNodes = d.nodes.map((nd, i) => ({ box: size[i], shape: nd.shape }));
    const wireEdges = d.edges.map((e) => ({ from: idx.get(e.from) ?? 0, to: idx.get(e.to) ?? 0, dashed: e.style === 'dashed' }));
    const zoneLabels = zones.map((z) => z.labelBox);
    const labelAll = (lines: Pt[][]) => {
      const free = [...taken];
      d.edges.forEach((e, k) => {
        if (!e.label) return;
        const spot = placeWireLabel(lines[k], labelW(e.label), free, lines, k);
        spots[k] = spot;
        free.push(spot.box);
      });
    };
    // Labels go where the wires leave room; the wires then keep off the labels, and the labels
    // settle beside where their wires ended up.
    let wires = routeWires(wireNodes, wireEdges, zoneLabels, lr);
    if (d.edges.some((e) => e.label)) {
      labelAll(wires.map((w) => w.route));
      wires = routeWires(wireNodes, wireEdges, [...zoneLabels, ...spots.flatMap((sp) => (sp ? [sp.box] : []))], lr);
      labelAll(wires.map((w) => w.route));
    }
    wires.forEach((w, k) => {
      const from = [...w.route[0]] as Pt;
      const to = [...w.route[w.route.length - 1]] as Pt;
      routes[k] = { from, mid: [(from[0] + to[0]) / 2, (from[1] + to[1]) / 2], to, pts: w.route, clean: true };
      dots[k] = w.dots;
      drawn.push(w.route);
    });
  } else {
    d.edges.forEach((_, k) => {
      const r = route(k, 0);
      if (r.clean) {
        routes[k] = r;
        drawn.push(r.pts);
      }
    });
    const straight = routes.map((r) => !!r);
    straight.forEach((ok, k) => ok && label(k));
    straight.forEach((ok, k) => {
      if (ok) return;
      const r = route(k, reach);
      routes[k] = r;
      drawn.push(r.pts);
    });
    straight.forEach((ok, k) => ok || label(k));
  }

  const edges: Laid['edges'] = d.edges.map((e, k) => {
    const r = routes[k]!;
    const spot = spots[k];
    return { key: `edge:${e.from}>${e.to}:${k}`, from: r.from, mid: r.mid, to: r.to, route: wired ? r.pts : null, dots: dots[k], dashed: e.style === 'dashed', color: arrowColor(e.from), label: e.label ?? '', labelBox: spot?.box ?? null, labelAlign: spot?.align ?? 'center' };
  });

  const nodes: Laid['nodes'] = d.nodes.map((nd, i) => {
    const inner = nd.shape === 'diamond' ? size[i].w / 2 : nd.shape === 'ellipse' ? size[i].w * 0.7 : size[i].w - 24;
    return { id: nd.id, box: size[i], shape: nd.shape, label: nd.label, note: nd.note ?? '', color: stroke(nd.group), lines: { label: wrapText(nd.label, inner), note: nd.note ? wrapText(nd.note, inner) : [] } };
  });

  // everything from (0, 0), the title above it
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  const grow = (x: number, y: number) => {
    minX = Math.min(minX, x);
    minY = Math.min(minY, y);
    maxX = Math.max(maxX, x);
    maxY = Math.max(maxY, y);
  };
  const growBox = (b: Box) => {
    grow(b.x, b.y);
    grow(b.x + b.w, b.y + b.h);
  };
  size.forEach(growBox);
  zones.forEach((z) => growBox(z.box));
  edges.forEach((e) => e.labelBox && growBox(e.labelBox));
  routes.forEach((r) => r!.pts.forEach((p) => grow(p[0], p[1])));
  if (minX === Infinity) minX = minY = maxX = maxY = 0;
  const sx = Math.ceil(-minX);
  const sy = Math.ceil(-minY) + (d.title ? TITLE_H : 0);
  const shift = (b: Box) => {
    b.x += sx;
    b.y += sy;
  };
  const shiftPt = (p: Pt) => {
    p[0] += sx;
    p[1] += sy;
  };
  size.forEach(shift);
  for (const z of zones) {
    shift(z.box);
    shift(z.labelBox);
  }
  for (const e of edges) {
    shiftPt(e.from);
    shiftPt(e.mid);
    shiftPt(e.to);
    e.route?.forEach(shiftPt);
    e.dots.forEach(shiftPt);
    if (e.labelBox) shift(e.labelBox);
  }
  const title = d.title ? { text: d.title, box: { x: 0, y: 0, w: Math.max(200, Math.ceil(textW(d.title) * 1.4) + 24), h: TITLE_BOX_H } } : null;
  let width = maxX + sx;
  const height = maxY + sy;
  if (title) width = Math.max(width, title.box.w);
  return { nodes, edges, zones, title, width: Math.ceil(width), height: Math.ceil(height) };
}

const seed = () => Math.floor(Math.random() * 2147483646) + 1;

/** Board nodes for a diagram with its top left at (ox, oy); z order zones → edges → nodes → labels → title. */
export function diagramNodes(d: Diagram, style: DiagramStyle, diagramId: string, ox: number, oy: number, look: DiagramLook = {}): BoardNode[] {
  const L = layoutDiagram(d, style, look);
  const out: BoardNode[] = [];
  const put = (kind: string, b: Box, data: Record<string, unknown>, key: string) =>
    out.push({
      id: newId('n'),
      kind,
      x: Math.round(ox + b.x),
      y: Math.round(oy + b.y),
      w: Math.max(1, Math.round(b.w)),
      h: Math.max(1, Math.round(b.h)),
      data: { ...data, diagramId, diagramKey: key },
    });
  for (const z of L.zones) {
    put('shape', z.box, { shape: 'rect', stroke: z.color, strokeWidth: 1, strokeStyle: 'dashed', roughness: 1, fill: 'transparent', fillStyle: 'hachure', edges: 'round', opacity: 70, text: '', seed: seed() }, `group:${z.id}`);
    put('text', z.labelBox, { text: z.label, font: 'hand', size: 's', align: 'left', color: z.color }, `groupLabel:${z.id}`);
  }
  const labels: { b: Box; text: string; key: string; align: string; color: string }[] = [];
  const labelColor = (e: Laid['edges'][number]) => (look.arrowColor === 'color' ? e.color : INK);
  L.edges.forEach((e, k) => {
    const xs = [e.from[0], e.mid[0], e.to[0], ...(e.route ?? []).map((p) => p[0])];
    const ys = [e.from[1], e.mid[1], e.to[1], ...(e.route ?? []).map((p) => p[1])];
    const minX = Math.min(...xs);
    const minY = Math.min(...ys);
    const r = (v: number) => Math.round(v * 10) / 10;
    const rel = (p: Pt): Pt => [r(p[0] - minX), r(p[1] - minY)];
    const pts = [
      [r(e.from[0] - minX), r(e.from[1] - minY)],
      [r(e.mid[0] - minX), r(e.mid[1] - minY)],
      [r(e.to[0] - minX), r(e.to[1] - minY)],
    ];
    put(
      'line',
      { x: Math.round(minX), y: Math.round(minY), w: Math.max(...xs) - minX, h: Math.max(...ys) - minY },
      {
        points: pts,
        // a client that does not know routes draws the straight line through `points`
        ...(e.route ? { route: e.route.map(rel), dots: e.dots.map(rel) } : {}),
        arrowStart: false,
        arrowEnd: true,
        stroke: e.color,
        strokeWidth: 2,
        strokeStyle: e.dashed ? 'dashed' : 'solid',
        roughness: e.route ? 0 : 1,
        opacity: 100,
        seed: seed(),
      },
      e.key,
    );
    if (e.label && e.labelBox) labels.push({ b: e.labelBox, text: e.label, key: `edgeLabel:${k}`, align: e.labelAlign, color: labelColor(e) });
  });
  for (const n of L.nodes) {
    const shape = n.shape === 'ellipse' ? 'ellipse' : n.shape === 'diamond' ? 'diamond' : 'rect';
    put(
      'shape',
      n.box,
      {
        shape,
        stroke: n.color,
        strokeWidth: 2,
        strokeStyle: 'solid',
        roughness: 1,
        fill: 'transparent',
        fillStyle: 'hachure',
        backed: true,
        edges: n.shape === 'box' ? 'sharp' : 'round',
        opacity: 100,
        text: n.note ? `${n.label}\n${n.note}` : n.label,
        font: 'hand',
        size: 'm',
        align: 'center',
        seed: seed(),
      },
      `node:${n.id}`,
    );
  }
  for (const l of labels) put('text', l.b, { text: l.text, font: 'hand', size: 's', align: l.align, color: l.color }, l.key);
  if (L.title) put('text', L.title.box, { text: L.title.text, font: 'hand', size: 'l', align: 'left', color: INK }, 'title');
  return out;
}

export function diagramSize(d: Diagram, style: DiagramStyle, look: DiagramLook = {}): { w: number; h: number } {
  const L = layoutDiagram(d, style, look);
  return { w: L.width, h: L.height };
}
