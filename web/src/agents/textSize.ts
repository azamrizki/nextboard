// Terminal text size: a per-client global scale (Settings › Appearance, localStorage)
// and an optional per-console override stored on the board node (data.textScale).

import { useSyncExternalStore } from 'react';
import { prefs } from '../lib/format';

export const TEXT_STEPS = [85, 100, 115, 130, 150, 175, 200] as const;
export const BASE_FONT_PX = 13;

const listeners = new Set<() => void>();
function read(): number {
  const n = Number(prefs.get('textScale'));
  return (TEXT_STEPS as readonly number[]).includes(n) ? n : 100;
}
let globalScale = read();

export function setGlobalTextScale(n: number) {
  globalScale = (TEXT_STEPS as readonly number[]).includes(n) ? n : 100;
  prefs.set('textScale', String(globalScale));
  listeners.forEach((l) => l());
}

export function useGlobalTextScale(): number {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => globalScale,
  );
}

/** Valid override from node data, or null (= follow the global setting). */
export function overrideOf(v: unknown): number | null {
  return typeof v === 'number' && (TEXT_STEPS as readonly number[]).includes(v) ? v : null;
}

export function stepScale(cur: number, dir: 1 | -1): number | null {
  const i = TEXT_STEPS.indexOf(cur as (typeof TEXT_STEPS)[number]);
  const j = (i < 0 ? 1 : i) + dir;
  return j >= 0 && j < TEXT_STEPS.length ? TEXT_STEPS[j] : null;
}

/** Whole pixels: xterm's glyph atlas renders crisper than with fractional sizes. */
export const fontPx = (scale: number) => Math.round((BASE_FONT_PX * scale) / 100);
