// What the browser tab says (DESIGN.md §3.4, Browser tab): before the page's title, the agents
// that need you or are idle (else those working) and the usage; the NextBoard mark as the icon,
// its top-right block lit for the agents (amber needs you, green idle, blue working) and its
// bottom bar filled to the usage (white, amber from 75%, red from 90%). A tab in the
// background, narrow or pinned, still tells you an agent finished.

import { useSyncExternalStore } from 'react';

export interface TabFacts {
  needsYou: number;
  idle: number;
  working: number;
  /** The session window used, null when unknown or too old. */
  usage: { pct: number; resets: string } | null;
  /** Live updates are lost: the counts may be old. */
  offline: boolean;
}

export type TitleShows = 'all' | 'agents' | 'none';

export interface TabPrefs {
  on: boolean;
  icon: boolean;
  title: TitleShows;
  /** Count the agents of every project, or of the project open in this tab. */
  scope: 'all' | 'project';
}

export const TAB_DEFAULTS: TabPrefs = { on: true, icon: true, title: 'all', scope: 'all' };

/** A usage report older than this is not shown in the tab: with automatic checks off it can be hours old. */
export const USAGE_FRESH_MS = 30 * 60_000;

/** "1 needs you · 2 idle · 45%", "" when there is nothing to say. */
export function statusText(f: TabFacts, shows: TitleShows): string {
  if (shows === 'none') return '';
  if (f.offline) return 'Reconnecting…';
  const parts: string[] = [];
  if (f.needsYou) parts.push(`${f.needsYou} ${f.needsYou === 1 ? 'needs' : 'need'} you`);
  if (f.idle) parts.push(`${f.idle} idle`);
  if (!f.needsYou && !f.idle && f.working) parts.push(`${f.working} working`);
  if (shows === 'all' && f.usage) {
    parts.push(`${f.usage.pct}%`);
    if (f.usage.pct >= 100 && f.usage.resets) parts.push(f.usage.resets);
  }
  return parts.join(' · ');
}

const LIGHT = { needs: '#fbbf24', idle: '#4ade80', working: '#60a5fa', none: '#8c8c8c' } as const;

/** The mark of index.html, lit for f. */
export function faviconSvg(f: TabFacts): string {
  const light = f.offline ? LIGHT.none : f.needsYou ? LIGHT.needs : f.idle ? LIGHT.idle : f.working ? LIGHT.working : LIGHT.none;
  let bar = `<rect x='5' y='17' width='22' height='8' rx='2' fill='#f5f5f5'/>`;
  if (f.usage && !f.offline) {
    const pct = Math.max(0, Math.min(100, f.usage.pct));
    const fill = pct >= 90 ? '#f87171' : pct >= 75 ? '#fbbf24' : '#f5f5f5';
    // never an empty bar for a little usage: the track alone would read as "no reading"
    const w = Math.max(2, Math.round((22 * pct) / 100));
    bar = `<rect x='5' y='17' width='22' height='8' rx='2' fill='#3a3a3a'/><rect x='5' y='17' width='${w}' height='8' rx='2' fill='${fill}'/>`;
  }
  const marks = `<rect width='32' height='32' rx='7' fill='#0a0a0a'/><rect x='5' y='7' width='10' height='8' rx='2' fill='#f5f5f5'/><rect x='17' y='7' width='10' height='8' rx='2' fill='${light}'/>${bar}`;
  const body = f.offline ? `<g opacity='.38'>${marks}</g>` : marks;
  return `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'>${body}</svg>`;
}

export function faviconHref(f: TabFacts): string {
  return `data:image/svg+xml,${encodeURIComponent(faviconSvg(f))}`;
}

// ---- Settings › Appearance (per browser) ---------------------------------------------------

const KEY = 'nb.tabStatus';
const listeners = new Set<() => void>();

function readPrefs(): TabPrefs {
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? '{}') as Partial<TabPrefs>;
    return {
      on: raw.on !== false,
      icon: raw.icon !== false,
      title: raw.title === 'agents' || raw.title === 'none' ? raw.title : 'all',
      scope: raw.scope === 'project' ? 'project' : 'all',
    };
  } catch {
    return TAB_DEFAULTS;
  }
}

let prefs = readPrefs();

export function setTabPrefs(patch: Partial<TabPrefs>): void {
  prefs = { ...prefs, ...patch };
  try {
    localStorage.setItem(KEY, JSON.stringify(prefs));
  } catch {
    /* storage blocked: this page keeps them */
  }
  listeners.forEach((l) => l());
}

export function useTabPrefs(): TabPrefs {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => prefs,
  );
}
