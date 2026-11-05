// Heavy drawing / rendering libraries are loaded on first use, so they stay out of the
// initial bundle: rough.js (shapes, lines), perfect-freehand (ink), highlight.js (code
// cards) and markdown-it + DOMPurify (markdown cards).

import { useEffect, useState } from 'react';
import type { RoughGenerator } from 'roughjs/bin/generator';
import type { getStroke as GetStroke } from 'perfect-freehand';

type Loader<T> = { value: T | null; promise: Promise<T> | null; listeners: Set<() => void> };

function loader<T>(load: () => Promise<T>) {
  const l: Loader<T> = { value: null, promise: null, listeners: new Set() };
  const ensure = (): Promise<T> => {
    if (!l.promise) {
      l.promise = load().then((v) => {
        l.value = v;
        l.listeners.forEach((fn) => fn());
        l.listeners.clear();
        return v;
      });
      l.promise.catch(() => {
        l.promise = null; // allow a retry later (e.g. after a network blip)
      });
    }
    return l.promise;
  };
  const use = (enabled = true): T | null => {
    const [, force] = useState(0);
    useEffect(() => {
      if (!enabled || l.value) return;
      const fn = () => force((n) => n + 1);
      l.listeners.add(fn);
      void ensure().catch(() => undefined);
      return () => {
        l.listeners.delete(fn);
      };
    }, [enabled]);
    return l.value;
  };
  return { ensure, use, get: () => l.value };
}

const rough = loader<RoughGenerator>(async () => {
  const m = await import('roughjs/bin/generator');
  return new m.RoughGenerator();
});
export const loadRough = rough.ensure;
export const useRough = rough.use;

const freehand = loader<typeof GetStroke>(async () => (await import('perfect-freehand')).getStroke);
export const loadFreehand = freehand.ensure;
export const useFreehand = freehand.use;
export const getFreehand = freehand.get;

const highlighter = loader(() => import('../lib/highlight'));
export const useHighlighter = highlighter.use;

const markdown = loader(() => import('../lib/markdown'));
export const useMarkdown = markdown.use;

/** SVG path data for a perfect-freehand outline polygon. */
export function outlineToPath(points: number[][]): string {
  if (points.length < 2) return '';
  const avg = (a: number, b: number) => (a + b) / 2;
  let d = `M${points[0][0].toFixed(1)},${points[0][1].toFixed(1)} Q`;
  for (let i = 0; i < points.length; i++) {
    const [x0, y0] = points[i];
    const [x1, y1] = points[(i + 1) % points.length];
    d += `${x0.toFixed(1)},${y0.toFixed(1)} ${avg(x0, x1).toFixed(1)},${avg(y0, y1).toFixed(1)} `;
  }
  return d + 'Z';
}

export const FREEHAND_OPTIONS = { thinning: 0.55, smoothing: 0.5, streamline: 0.45 } as const;

/** Ink stroke → SVG path (filled outline), or a plain polyline while the lib loads. */
export function inkPath(points: [number, number, number][], width: number, getStroke: typeof GetStroke | null): string {
  if (points.length === 0) return '';
  if (getStroke) {
    const simulate = points.every((p) => p[2] === 0.5 || p[2] === 0);
    return outlineToPath(getStroke(points, { size: width, ...FREEHAND_OPTIONS, simulatePressure: simulate, last: true }));
  }
  return 'M' + points.map((p) => `${p[0].toFixed(1)},${p[1].toFixed(1)}`).join(' L');
}
