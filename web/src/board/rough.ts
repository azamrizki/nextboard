// rough.js drawing for shape and line tiles → plain SVG path descriptors (rendered by React).

import type { RoughGenerator } from 'roughjs/bin/generator';
import type { Options } from 'roughjs/bin/core';
import { controlPoint, isStraight } from './geometry';
import type { LineData, Pt, ShapeData, StrokeData } from './kinds';

export interface SvgPath {
  d: string;
  stroke: string;
  strokeWidth: number;
  fill: string;
  /** Outline (gets the dash pattern) vs. fill strokes. */
  outline: boolean;
}

export function dashArray(style: StrokeData['strokeStyle'], w: number): string | undefined {
  if (style === 'dashed') return `${w * 4 + 4} ${w * 3 + 4}`;
  if (style === 'dotted') return `${Math.max(1, w * 0.6)} ${w * 2.5 + 3}`;
  return undefined;
}

function baseOptions(d: StrokeData): Options {
  return {
    stroke: d.stroke,
    strokeWidth: d.strokeWidth,
    roughness: d.roughness === 0 ? 0 : d.roughness === 1 ? 1.1 : 2.2,
    bowing: d.roughness === 0 ? 0 : 1,
    seed: Math.max(1, d.seed % 2147483647),
    disableMultiStroke: d.roughness === 0 || d.strokeStyle !== 'solid',
    preserveVertices: d.roughness === 0,
  };
}

function toSvg(gen: RoughGenerator, drawable: ReturnType<RoughGenerator['path']>, outlineColor: string): SvgPath[] {
  return gen.toPaths(drawable).map((p) => ({
    d: p.d,
    stroke: p.stroke,
    strokeWidth: p.strokeWidth,
    fill: p.fill ?? 'none',
    outline: p.stroke === outlineColor && (p.fill ?? 'none') === 'none',
  }));
}

/** Rounded polygon path (corner radius r) through the given points. */
function roundedPolygon(pts: Pt[], r: number): string {
  const n = pts.length;
  let d = '';
  for (let i = 0; i < n; i++) {
    const p = pts[i];
    const prev = pts[(i - 1 + n) % n];
    const next = pts[(i + 1) % n];
    const v1 = [prev[0] - p[0], prev[1] - p[1]];
    const v2 = [next[0] - p[0], next[1] - p[1]];
    const l1 = Math.hypot(v1[0], v1[1]);
    const l2 = Math.hypot(v2[0], v2[1]);
    const rr = Math.min(r, l1 / 2, l2 / 2);
    const a = [p[0] + (v1[0] / l1) * rr, p[1] + (v1[1] / l1) * rr];
    const b = [p[0] + (v2[0] / l2) * rr, p[1] + (v2[1] / l2) * rr];
    d += `${i === 0 ? 'M' : 'L'}${a[0].toFixed(1)} ${a[1].toFixed(1)} Q${p[0].toFixed(1)} ${p[1].toFixed(1)} ${b[0].toFixed(1)} ${b[1].toFixed(1)} `;
  }
  return d + 'Z';
}

/** Geometry of a shape inside a w×h box (also used for the hit area). */
export function shapeOutline(shape: ShapeData['shape'], w: number, h: number, pad: number, round: boolean): string {
  const x0 = pad;
  const y0 = pad;
  const x1 = Math.max(pad + 1, w - pad);
  const y1 = Math.max(pad + 1, h - pad);
  if (shape === 'ellipse') {
    const rx = (x1 - x0) / 2;
    const ry = (y1 - y0) / 2;
    const cx = x0 + rx;
    const cy = y0 + ry;
    return `M${cx - rx} ${cy} A${rx} ${ry} 0 1 0 ${cx + rx} ${cy} A${rx} ${ry} 0 1 0 ${cx - rx} ${cy} Z`;
  }
  const pts: Pt[] =
    shape === 'diamond'
      ? [
          [(x0 + x1) / 2, y0],
          [x1, (y0 + y1) / 2],
          [(x0 + x1) / 2, y1],
          [x0, (y0 + y1) / 2],
        ]
      : [
          [x0, y0],
          [x1, y0],
          [x1, y1],
          [x0, y1],
        ];
  const r = round ? Math.min(24, Math.min(x1 - x0, y1 - y0) * (shape === 'diamond' ? 0.18 : 0.14)) : 0;
  return r > 0 ? roundedPolygon(pts, r) : 'M' + pts.map((p) => `${p[0]} ${p[1]}`).join(' L') + ' Z';
}

export function drawShape(gen: RoughGenerator, d: ShapeData, w: number, h: number): SvgPath[] {
  const pad = d.strokeWidth + 3;
  const o: Options = {
    ...baseOptions(d),
    fill: d.fill === 'transparent' ? undefined : d.fill,
    fillStyle: d.fillStyle,
    hachureGap: 5 + d.strokeWidth * 2,
    fillWeight: Math.max(1, d.strokeWidth * 0.6),
  };
  let drawable;
  if (d.shape === 'ellipse') {
    drawable = gen.ellipse(w / 2, h / 2, Math.max(2, w - pad * 2), Math.max(2, h - pad * 2), o);
  } else if (d.shape === 'rect' && d.edges === 'sharp') {
    drawable = gen.rectangle(pad, pad, Math.max(2, w - pad * 2), Math.max(2, h - pad * 2), o);
  } else {
    drawable = gen.path(shapeOutline(d.shape, w, h, pad, d.edges === 'round'), o);
  }
  return toSvg(gen, drawable, d.stroke);
}

/** Path for the line itself (straight or quadratic through the middle knob). */
export function linePathD(pts: [Pt, Pt, Pt]): string {
  const [p0, mid, p2] = pts;
  if (isStraight(p0, mid, p2)) return `M${p0[0]} ${p0[1]} L${p2[0]} ${p2[1]}`;
  const c = controlPoint(p0, mid, p2);
  return `M${p0[0]} ${p0[1]} Q${c[0]} ${c[1]} ${p2[0]} ${p2[1]}`;
}

/** What a line tile draws: a wire's stretches, or the curve through its middle knob. */
export function lineDrawnD(d: LineData): string {
  if (!d.route) return linePathD(d.points);
  return 'M' + d.route.map((p) => `${p[0]} ${p[1]}`).join(' L');
}

function arrowHead(tip: Pt, from: Pt, w: number): Pt[] {
  const a = Math.atan2(tip[1] - from[1], tip[0] - from[0]);
  const len = 10 + w * 3;
  const spread = (28 * Math.PI) / 180;
  return [
    [tip[0] - Math.cos(a - spread) * len, tip[1] - Math.sin(a - spread) * len],
    tip,
    [tip[0] - Math.cos(a + spread) * len, tip[1] - Math.sin(a + spread) * len],
  ];
}

export function arrowHeads(d: LineData): Pt[][] {
  if (d.route) {
    const r = d.route;
    const heads: Pt[][] = [];
    if (d.arrowEnd) heads.push(arrowHead(r[r.length - 1], r[r.length - 2], d.strokeWidth));
    if (d.arrowStart) heads.push(arrowHead(r[0], r[1], d.strokeWidth));
    return heads;
  }
  const [p0, mid, p2] = d.points;
  const c = isStraight(p0, mid, p2) ? null : controlPoint(p0, mid, p2);
  const out: Pt[][] = [];
  if (d.arrowEnd) out.push(arrowHead(p2, c ?? p0, d.strokeWidth));
  if (d.arrowStart) out.push(arrowHead(p0, c ?? p2, d.strokeWidth));
  return out;
}

export function drawLine(gen: RoughGenerator, d: LineData): SvgPath[] {
  const o = baseOptions(d);
  const out = toSvg(gen, gen.path(lineDrawnD(d), o), d.stroke);
  for (const head of arrowHeads(d)) out.push(...toSvg(gen, gen.linearPath(head as [number, number][], o), d.stroke));
  return out;
}
