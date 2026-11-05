// Diagram exports from the as-generated nb.diagram/v1 (docs/API-studio.md §6, E3): PNG at
// 2× on the board's dark background, and an Excalidraw file (an open JSON format).
// Later edits to the landed shapes are not part of the export, as in the spec.

import { inkColor } from '../../board/palette';
import type { Diagram, DiagramStyle } from '../types';
import { layoutDiagram, type DiagramLook } from './layout';

const PAD = 40;

function cssVar(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

/** A stored colour as this theme paints it: chalk on a white background is dark ink, as on the board. */
function painted(stored: string): string {
  const v = inkColor(stored);
  const token = /^var\((--[\w-]+)\)$/.exec(v);
  return token ? cssVar(token[1], stored) : v;
}

export async function diagramPng(d: Diagram, style: DiagramStyle, look: DiagramLook = {}): Promise<Blob> {
  const L = layoutDiagram(d, style, look);
  const scale = 2;
  const canvas = document.createElement('canvas');
  canvas.width = (L.width + PAD * 2) * scale;
  canvas.height = (L.height + PAD * 2) * scale;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('This browser can’t draw images.');
  try {
    await document.fonts?.load('20px Caveat');
  } catch {
    /* system fallback font */
  }
  const font = cssVar('--font-hand', 'cursive');
  ctx.scale(scale, scale);
  ctx.fillStyle = cssVar('--bg', 'black');
  ctx.fillRect(0, 0, L.width + PAD * 2, L.height + PAD * 2);
  ctx.translate(PAD, PAD);
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';
  for (const z of L.zones) {
    ctx.save();
    ctx.setLineDash([8, 6]);
    ctx.strokeStyle = painted(z.color);
    ctx.globalAlpha = 0.7;
    ctx.lineWidth = 1;
    roundRect(ctx, z.box.x, z.box.y, z.box.w, z.box.h, 14);
    ctx.stroke();
    ctx.restore();
    ctx.fillStyle = painted(z.color);
    ctx.font = `17.5px ${font}`;
    ctx.textBaseline = 'top';
    ctx.fillText(z.label, z.box.x + 10, z.box.y + 6);
  }
  for (const e of L.edges) {
    ctx.save();
    ctx.strokeStyle = painted(e.color);
    ctx.fillStyle = painted(e.color);
    ctx.lineWidth = 2;
    if (e.dashed) ctx.setLineDash([8, 6]);
    ctx.beginPath();
    ctx.moveTo(e.from[0], e.from[1]);
    if (e.route) for (const p of e.route.slice(1)) ctx.lineTo(p[0], p[1]);
    else ctx.quadraticCurveTo(2 * e.mid[0] - (e.from[0] + e.to[0]) / 2, 2 * e.mid[1] - (e.from[1] + e.to[1]) / 2, e.to[0], e.to[1]);
    ctx.stroke();
    ctx.setLineDash([]);
    for (const p of e.dots) {
      ctx.beginPath();
      ctx.arc(p[0], p[1], 4, 0, Math.PI * 2);
      ctx.fill();
    }
    const before = e.route ? e.route[e.route.length - 2] : e.mid;
    const ang = Math.atan2(e.to[1] - before[1], e.to[0] - before[0]);
    ctx.beginPath();
    ctx.moveTo(e.to[0], e.to[1]);
    ctx.lineTo(e.to[0] - 12 * Math.cos(ang - 0.4), e.to[1] - 12 * Math.sin(ang - 0.4));
    ctx.lineTo(e.to[0] - 12 * Math.cos(ang + 0.4), e.to[1] - 12 * Math.sin(ang + 0.4));
    ctx.closePath();
    ctx.fill();
    ctx.restore();
  }
  for (const n of L.nodes) {
    const { x, y, w, h } = n.box;
    ctx.save();
    ctx.strokeStyle = painted(n.color);
    ctx.lineWidth = 2;
    ctx.beginPath();
    if (n.shape === 'ellipse') ctx.ellipse(x + w / 2, y + h / 2, w / 2, h / 2, 0, 0, Math.PI * 2);
    else if (n.shape === 'diamond') {
      ctx.moveTo(x + w / 2, y);
      ctx.lineTo(x + w, y + h / 2);
      ctx.lineTo(x + w / 2, y + h);
      ctx.lineTo(x, y + h / 2);
      ctx.closePath();
    } else roundRect(ctx, x, y, w, h, n.shape === 'box' ? 2 : 14);
    ctx.fillStyle = cssVar('--bg', 'black'); // what runs behind a node does not cross its text
    ctx.fill();
    ctx.stroke();
    ctx.restore();
    ctx.fillStyle = painted(n.color);
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.font = `25px ${font}`;
    const lines = [...n.lines.label, ...n.lines.note];
    lines.forEach((line, i) => {
      ctx.globalAlpha = i < n.lines.label.length ? 1 : 0.8;
      ctx.fillText(line, x + w / 2, y + h / 2 + (i - (lines.length - 1) / 2) * 31.25);
    });
    ctx.globalAlpha = 1;
  }
  for (const e of L.edges) {
    if (!e.label || !e.labelBox) continue;
    const b = e.labelBox;
    ctx.font = `17.5px ${font}`;
    ctx.fillStyle = painted(e.color);
    ctx.textAlign = e.labelAlign;
    ctx.fillText(e.label, e.labelAlign === 'left' ? b.x + 4 : e.labelAlign === 'right' ? b.x + b.w - 4 : b.x + b.w / 2, b.y + b.h / 2);
  }
  if (L.title) {
    ctx.textAlign = 'left';
    ctx.textBaseline = 'top';
    ctx.fillStyle = cssVar('--text', 'white');
    ctx.font = `35px ${font}`;
    ctx.fillText(L.title.text, L.title.box.x, L.title.box.y);
  }
  return new Promise((resolve, reject) => canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('PNG export failed'))), 'image/png'));
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  const rr = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + rr, y);
  ctx.arcTo(x + w, y, x + w, y + h, rr);
  ctx.arcTo(x + w, y + h, x, y + h, rr);
  ctx.arcTo(x, y + h, x, y, rr);
  ctx.arcTo(x, y, x + w, y, rr);
  ctx.closePath();
}

let counter = 0;
const rid = () => `nb${Date.now().toString(36)}${(counter++).toString(36)}`;
const seed = () => Math.floor(Math.random() * 2 ** 31);

function base(type: string, x: number, y: number, w: number, h: number, stroke: string) {
  return {
    id: rid(),
    type,
    x,
    y,
    width: w,
    height: h,
    angle: 0,
    strokeColor: stroke,
    backgroundColor: 'transparent',
    fillStyle: 'hachure',
    strokeWidth: 2,
    strokeStyle: 'solid',
    roughness: 1,
    opacity: 100,
    groupIds: [] as string[],
    frameId: null,
    roundness: null as null | { type: number },
    seed: seed(),
    version: 1,
    versionNonce: seed(),
    isDeleted: false,
    boundElements: null as null | { type: string; id: string }[],
    updated: Date.now(),
    link: null,
    locked: false,
  };
}

function text(x: number, y: number, w: number, h: number, value: string, color: string, size: number, align: 'left' | 'center', containerId: string | null = null) {
  return {
    ...base('text', x, y, w, h, color),
    text: value,
    fontSize: size,
    fontFamily: 1,
    textAlign: align,
    verticalAlign: containerId ? 'middle' : 'top',
    containerId,
    originalText: value,
    autoResize: true,
    lineHeight: 1.25,
  };
}

/** Excalidraw scene JSON (type "excalidraw", version 2). */
export function diagramExcalidraw(d: Diagram, style: DiagramStyle, look: DiagramLook = {}): string {
  const L = layoutDiagram(d, style, look);
  const els: object[] = [];
  for (const z of L.zones) {
    els.push({ ...base('rectangle', z.box.x, z.box.y, z.box.w, z.box.h, painted(z.color)), strokeWidth: 1, strokeStyle: 'dashed', opacity: 70, roundness: { type: 3 } });
    els.push(text(z.labelBox.x, z.labelBox.y, z.labelBox.w, z.labelBox.h, z.label, painted(z.color), 16, 'left'));
  }
  for (const e of L.edges) {
    const color = painted(e.color);
    const pts = (e.route ?? [e.from, e.mid, e.to]).map((p) => [p[0] - e.from[0], p[1] - e.from[1]]);
    const xs = pts.map((p) => p[0]);
    const ys = pts.map((p) => p[1]);
    els.push({
      ...base('arrow', e.from[0], e.from[1], Math.max(...xs) - Math.min(...xs), Math.max(...ys) - Math.min(...ys), color),
      strokeStyle: e.dashed ? 'dashed' : 'solid',
      roundness: e.route ? null : { type: 2 },
      roughness: e.route ? 0 : 1,
      points: pts,
      lastCommittedPoint: null,
      startBinding: null,
      endBinding: null,
      startArrowhead: null,
      endArrowhead: 'arrow',
    });
    for (const p of e.dots) els.push({ ...base('ellipse', p[0] - 4, p[1] - 4, 8, 8, color), backgroundColor: color, fillStyle: 'solid', roughness: 0 });
    if (e.label && e.labelBox) els.push(text(e.labelBox.x, e.labelBox.y, e.labelBox.w, e.labelBox.h, e.label, color, 14, e.labelAlign === 'center' ? 'center' : 'left'));
  }
  for (const n of L.nodes) {
    const type = n.shape === 'ellipse' ? 'ellipse' : n.shape === 'diamond' ? 'diamond' : 'rectangle';
    const color = painted(n.color);
    const shape = { ...base(type, n.box.x, n.box.y, n.box.w, n.box.h, color), backgroundColor: cssVar('--bg', 'black'), fillStyle: 'solid', roundness: n.shape === 'round' ? { type: 3 } : null };
    // 16 px: Excalidraw's hand font is wider than this board's
    const label = { ...text(n.box.x + 8, n.box.y + 8, n.box.w - 16, n.box.h - 16, [...n.lines.label, ...n.lines.note].join('\n'), color, 16, 'center', shape.id), originalText: n.note ? `${n.label}\n${n.note}` : n.label };
    shape.boundElements = [{ type: 'text', id: label.id }];
    els.push(shape, label);
  }
  if (L.title) els.push(text(L.title.box.x, L.title.box.y, L.title.box.w, L.title.box.h, L.title.text, cssVar('--text', 'white'), 28, 'left'));
  return JSON.stringify({ type: 'excalidraw', version: 2, source: 'NextBoard', elements: els, appState: { viewBackgroundColor: cssVar('--bg', 'black'), gridSize: null }, files: {} }, null, 2);
}
