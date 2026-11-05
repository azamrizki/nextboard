// Geometry helpers: line/arrow bounding boxes, hit tests for the eraser, bounding boxes
// of selections, alignment guides while dragging.

import type { BoardNode, NodeLayout } from '../lib/types';
import { readInk, readLine, type Pt } from './kinds';

/** Margin around line points so arrowheads, strokes and knobs stay inside the node. */
export const LINE_PAD = 24;

/** Absolute points → node rect + points relative to it. */
export function normalizeLine(abs: [Pt, Pt, Pt], pad = LINE_PAD): { rect: NodeLayout; points: [Pt, Pt, Pt] } {
  const xs = abs.map((p) => p[0]);
  const ys = abs.map((p) => p[1]);
  const x = Math.round(Math.min(...xs) - pad);
  const y = Math.round(Math.min(...ys) - pad);
  const w = Math.max(1, Math.round(Math.max(...xs) - Math.min(...xs) + pad * 2));
  const h = Math.max(1, Math.round(Math.max(...ys) - Math.min(...ys) + pad * 2));
  const rel = abs.map((p) => [Math.round((p[0] - x) * 10) / 10, Math.round((p[1] - y) * 10) / 10] as Pt) as [Pt, Pt, Pt];
  return { rect: { x, y, w, h }, points: rel };
}

/** Absolute points of a wire → node rect + what its data holds, relative to that rect. */
export function normalizeRoute(abs: Pt[], dots: Pt[], pad = LINE_PAD): { rect: NodeLayout; points: [Pt, Pt, Pt]; route: Pt[]; dots: Pt[] } {
  const xs = abs.map((p) => p[0]);
  const ys = abs.map((p) => p[1]);
  const x = Math.round(Math.min(...xs) - pad);
  const y = Math.round(Math.min(...ys) - pad);
  const w = Math.max(1, Math.round(Math.max(...xs) - Math.min(...xs) + pad * 2));
  const h = Math.max(1, Math.round(Math.max(...ys) - Math.min(...ys) + pad * 2));
  const rel = (p: Pt): Pt => [Math.round((p[0] - x) * 10) / 10, Math.round((p[1] - y) * 10) / 10];
  const a = abs[0];
  const b = abs[abs.length - 1];
  return { rect: { x, y, w, h }, points: [rel(a), rel([(a[0] + b[0]) / 2, (a[1] + b[1]) / 2]), rel(b)], route: abs.map(rel), dots: dots.map(rel) };
}

/** A wire with one tip somewhere else: the stretch at that tip moves along, the wire stays straight stretches. */
export function moveWireTip(route: Pt[], last: boolean, p: Pt): Pt[] {
  const r = route.map((q) => [q[0], q[1]] as Pt);
  const n = r.length;
  const flat = r[0][1] === r[1][1];
  if (n === 2) {
    const a = last ? r[0] : p;
    const b = last ? p : r[1];
    if (a[0] === b[0] || a[1] === b[1]) return [a, b];
    if (flat) {
      const x = (a[0] + b[0]) / 2;
      return [a, [x, a[1]], [x, b[1]], b];
    }
    const y = (a[1] + b[1]) / 2;
    return [a, [a[0], y], [b[0], y], b];
  }
  const tip = last ? n - 1 : 0;
  const next = last ? n - 2 : 1;
  const level = r[tip][1] === r[next][1];
  r[tip] = p;
  if (level) r[next][1] = p[1];
  else r[next][0] = p[0];
  return r;
}

/** A wire with stretch i (between points i and i + 1) pushed sideways to `p`; both neighbours stretch with it. */
export function moveWireStretch(route: Pt[], i: number, p: Pt): Pt[] {
  const r = route.map((q) => [q[0], q[1]] as Pt);
  if (r[i][1] === r[i + 1][1]) r[i][1] = r[i + 1][1] = p[1];
  else r[i][0] = r[i + 1][0] = p[0];
  return r;
}

/** Control point of the quadratic curve that passes through `mid` at t = 0.5. */
export function controlPoint(p0: Pt, mid: Pt, p2: Pt): Pt {
  return [2 * mid[0] - (p0[0] + p2[0]) / 2, 2 * mid[1] - (p0[1] + p2[1]) / 2];
}

export function isStraight(p0: Pt, mid: Pt, p2: Pt): boolean {
  const mx = (p0[0] + p2[0]) / 2;
  const my = (p0[1] + p2[1]) / 2;
  return Math.hypot(mid[0] - mx, mid[1] - my) < 1.5;
}

/** Snap `p` around `origin` to 15° steps (Shift while dragging a tip). */
export function snapAngle(origin: Pt, p: Pt, stepDeg = 15): Pt {
  const dx = p[0] - origin[0];
  const dy = p[1] - origin[1];
  const len = Math.hypot(dx, dy);
  if (len === 0) return p;
  const step = (stepDeg * Math.PI) / 180;
  const a = Math.round(Math.atan2(dy, dx) / step) * step;
  return [origin[0] + Math.cos(a) * len, origin[1] + Math.sin(a) * len];
}

export function distToSegment(p: Pt, a: Pt, b: Pt): number {
  const dx = b[0] - a[0];
  const dy = b[1] - a[1];
  const l2 = dx * dx + dy * dy;
  let t = l2 ? ((p[0] - a[0]) * dx + (p[1] - a[1]) * dy) / l2 : 0;
  t = Math.max(0, Math.min(1, t));
  return Math.hypot(p[0] - (a[0] + t * dx), p[1] - (a[1] + t * dy));
}

function distToPolyline(p: Pt, pts: Pt[]): number {
  if (pts.length === 1) return Math.hypot(p[0] - pts[0][0], p[1] - pts[0][1]);
  let best = Infinity;
  for (let i = 1; i < pts.length; i++) best = Math.min(best, distToSegment(p, pts[i - 1], pts[i]));
  return best;
}

/** Sample a quadratic curve through p0 – mid – p2. */
export function sampleCurve(p0: Pt, mid: Pt, p2: Pt, steps = 16): Pt[] {
  const c = controlPoint(p0, mid, p2);
  const out: Pt[] = [];
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    const u = 1 - t;
    out.push([u * u * p0[0] + 2 * u * t * c[0] + t * t * p2[0], u * u * p0[1] + 2 * u * t * c[1] + t * t * p2[1]]);
  }
  return out;
}

/** Is `p` on one of a wire's stretches? */
export function onRoute(p: Pt, route: Pt[]): boolean {
  return distToPolyline(p, route) < 1;
}

const inRect = (p: Pt, n: NodeLayout) => p[0] >= n.x && p[0] <= n.x + n.w && p[1] >= n.y && p[1] <= n.y + n.h;

/** Does a point (board coords) hit this node? Strokes and lines are hit near their path. */
export function hitNode(n: BoardNode, p: Pt, tolerance: number): boolean {
  if (!inRect(p, { x: n.x - tolerance, y: n.y - tolerance, w: n.w + tolerance * 2, h: n.h + tolerance * 2 })) return false;
  const local: Pt = [p[0] - n.x, p[1] - n.y];
  if (n.kind === 'ink') {
    const d = readInk(n);
    return distToPolyline(local, d.points.map((q) => [q[0], q[1]] as Pt)) <= d.width / 2 + tolerance;
  }
  if (n.kind === 'line') {
    const d = readLine(n);
    return distToPolyline(local, d.route ?? sampleCurve(d.points[0], d.points[1], d.points[2])) <= d.strokeWidth / 2 + tolerance;
  }
  return inRect(p, n);
}

export function boundsOf(rects: NodeLayout[]): NodeLayout | null {
  if (rects.length === 0) return null;
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const r of rects) {
    x0 = Math.min(x0, r.x);
    y0 = Math.min(y0, r.y);
    x1 = Math.max(x1, r.x + r.w);
    y1 = Math.max(y1, r.y + r.h);
  }
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

export interface Guide {
  /** 'v' = vertical line at x; 'h' = horizontal line at y. */
  axis: 'v' | 'h';
  pos: number;
  from: number;
  to: number;
}

/**
 * Alignment guides for a moving box against the other nodes (edges and centres within
 * `threshold` board px). Guides only — nothing snaps.
 */
export function alignmentGuides(box: NodeLayout, others: NodeLayout[], threshold: number): Guide[] {
  const xsBox = [box.x, box.x + box.w / 2, box.x + box.w];
  const ysBox = [box.y, box.y + box.h / 2, box.y + box.h];
  let bestV: Guide | null = null;
  let bestVd = threshold + 1;
  let bestH: Guide | null = null;
  let bestHd = threshold + 1;
  for (const o of others) {
    const xs = [o.x, o.x + o.w / 2, o.x + o.w];
    const ys = [o.y, o.y + o.h / 2, o.y + o.h];
    for (const a of xsBox)
      for (const b of xs) {
        const d = Math.abs(a - b);
        if (d <= threshold && d < bestVd) {
          bestVd = d;
          bestV = { axis: 'v', pos: b, from: Math.min(box.y, o.y), to: Math.max(box.y + box.h, o.y + o.h) };
        }
      }
    for (const a of ysBox)
      for (const b of ys) {
        const d = Math.abs(a - b);
        if (d <= threshold && d < bestHd) {
          bestHd = d;
          bestH = { axis: 'h', pos: b, from: Math.min(box.x, o.x), to: Math.max(box.x + box.w, o.x + o.w) };
        }
      }
  }
  return [bestV, bestH].filter((g): g is Guide => !!g);
}
