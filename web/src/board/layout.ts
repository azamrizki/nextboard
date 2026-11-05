// Node geometry helpers: default sizes, non-overlapping placement, terminal size guess.

import type { NodeLayout } from '../lib/types';

export const DEFAULT_W = 720;
export const DEFAULT_H = 440;
export const MIN_W = 360;
export const MIN_H = 220;
/** Minimap size in px; keep in sync with --minimap-w / --minimap-h in tokens.css. */
export const MINIMAP_W = 180;
export const MINIMAP_H = 120;
const GAP = 40;
const GRID_COLS = 3;

export type Rect = NodeLayout;

function overlaps(a: Rect, b: Rect, margin = GAP / 2): boolean {
  return (
    a.x < b.x + b.w + margin &&
    a.x + a.w + margin > b.x &&
    a.y < b.y + b.h + margin &&
    a.y + a.h + margin > b.y
  );
}

function free(r: Rect, existing: Rect[]): boolean {
  return !existing.some((e) => overlaps(r, e));
}

/** First grid cell (row-major, GRID_COLS wide) that doesn't overlap any existing node. */
export function gridPlace(existing: Rect[], w = DEFAULT_W, h = DEFAULT_H): Rect {
  for (let i = 0; i < 1000; i++) {
    const col = i % GRID_COLS;
    const row = Math.floor(i / GRID_COLS);
    const r = { x: col * (DEFAULT_W + GAP), y: row * (DEFAULT_H + GAP), w, h };
    if (free(r, existing)) return r;
  }
  const maxY = existing.reduce((m, e) => Math.max(m, e.y + e.h), 0);
  return { x: 0, y: maxY + GAP, w, h };
}

/** Place a node centred on `center` (flow coordinates), nudging outward until it's free. */
export function placeNear(existing: Rect[], center: { x: number; y: number }, w = DEFAULT_W, h = DEFAULT_H): Rect {
  const base = { x: Math.round(center.x - w / 2), y: Math.round(center.y - h / 2), w, h };
  if (free(base, existing)) return base;
  const stepX = Math.round(w / 3);
  const stepY = Math.round(h / 3);
  const dirs = [
    [1, 0],
    [0, 1],
    [-1, 0],
    [0, -1],
    [1, 1],
    [-1, 1],
    [1, -1],
    [-1, -1],
  ];
  for (let ring = 1; ring <= 30; ring++) {
    for (const [dx, dy] of dirs) {
      const r = { ...base, x: base.x + dx * ring * stepX, y: base.y + dy * ring * stepY };
      if (free(r, existing)) return r;
    }
  }
  return { ...base, x: base.x + 40, y: base.y + 40 };
}

/**
 * Rough cols/rows for a node of the given size before xterm has measured anything
 * (13px JetBrains Mono ≈ 7.8 × 17 px cells; header 38px; body padding 8px).
 */
export function guessTermSize(w = DEFAULT_W, h = DEFAULT_H): { cols: number; rows: number } {
  const cols = Math.max(20, Math.floor((w - 18) / 7.8));
  const rows = Math.max(5, Math.floor((h - 38 - 18) / 17));
  return { cols, rows };
}
