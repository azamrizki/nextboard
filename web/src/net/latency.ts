// The round trip from this page to the hub (docs/API.md, Connection to the hub): while the page
// is shown, a ping over /api/events every 15 s, its pong timed. The header's signal bars and the
// connection details read it; Settings › Appearance can hide the number.

import { useSyncExternalStore } from 'react';

export const PING_EVERY = 15_000;
/** A ping with no pong by then counts as lost. */
export const PING_TIMEOUT = 10_000;
/** Ten minutes of pings. */
const KEEP = 40;

export type NetLevel = 'good' | 'fair' | 'poor' | 'none';

export interface NetState {
  /** The last round trip in ms, null before the first answer. */
  rtt: number | null;
  /** The last round trips, oldest first; null is a ping that got no answer. */
  samples: (number | null)[];
  /** When the live connection opened (ms since the epoch), 0 while it is closed. */
  since: number;
  /** How many times it opened again since this page loaded. */
  reconnects: number;
}

interface Sender {
  sendJSON(value: unknown): boolean;
}

const EMPTY: NetState = { rtt: null, samples: [], since: 0, reconnects: 0 };
let state: NetState = EMPTY;
const listeners = new Set<() => void>();
let sender: Sender | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
let seq = 0;
let opens = 0;
const pending = new Map<number, number>();
let watching = false;

function set(patch: Partial<NetState>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

const hidden = () => typeof document !== 'undefined' && document.hidden;

function watchShown() {
  if (watching || typeof document === 'undefined') return;
  watching = true;
  // a page shown again measures at once: the number it shows is a fresh one
  document.addEventListener('visibilitychange', () => {
    if (!hidden() && sender && timer === undefined) ping();
  });
}

function schedule(ms: number) {
  if (timer !== undefined) clearTimeout(timer);
  timer = setTimeout(ping, ms);
}

function ping() {
  timer = undefined;
  if (!sender || hidden()) return;
  const id = ++seq;
  pending.set(id, performance.now());
  if (!sender.sendJSON({ type: 'ping', id })) {
    pending.delete(id);
    return;
  }
  setTimeout(() => {
    if (pending.delete(id)) set({ samples: [...state.samples, null].slice(-KEEP) });
  }, PING_TIMEOUT);
  schedule(PING_EVERY);
}

/** The live connection opened: measure now and then every 15 s. */
export function netOpened(s: Sender): void {
  sender = s;
  opens++;
  set({ since: Date.now(), reconnects: Math.max(0, opens - 1) });
  watchShown();
  schedule(0);
}

/** The live connection closed: no number until it is back. */
export function netClosed(): void {
  if (!sender && !state.since) return;
  sender = null;
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
  pending.clear();
  set({ since: 0, rtt: null });
}

/** A pong from the hub. */
export function netPong(id: number): void {
  const t0 = pending.get(id);
  if (t0 === undefined) return;
  pending.delete(id);
  const rtt = Math.max(1, Math.round(performance.now() - t0));
  set({ rtt, samples: [...state.samples, rtt].slice(-KEEP) });
}

/** Measure now (the details just opened), unless a ping is already out. */
export function netMeasureNow(): void {
  if (sender && pending.size === 0) ping();
}

/** For tests. */
export function resetNet(): void {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
  sender = null;
  pending.clear();
  opens = 0;
  seq = 0;
  state = EMPTY;
  listeners.forEach((l) => l());
}

export function getNet(): NetState {
  return state;
}

export function useNet(): NetState {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => state,
  );
}

// ---- what the numbers mean ---------------------------------------------------------------

/** How good the round trip is: under 100 ms good, to 300 ms fair, above (or pings lost) poor. */
export function netLevel(rtt: number | null, samples: readonly (number | null)[]): NetLevel {
  if (rtt == null) return 'none';
  const lost = samples.slice(-4).filter((x) => x === null).length;
  if (lost >= 2 || rtt > 300) return 'poor';
  if (lost === 1 || rtt >= 100) return 'fair';
  return 'good';
}

/** Signal bars, 1 to 4: under 60 ms all four, under 100 three, to 300 two, else one. */
export function netBars(rtt: number | null, level: NetLevel): number {
  if (rtt == null || level === 'none') return 0;
  if (level === 'poor') return 1;
  if (level === 'fair') return 2;
  return rtt < 60 ? 4 : 3;
}

/** How much the round trip moves: the mean difference between one ping and the next, in ms. */
export function netJitter(samples: readonly (number | null)[]): number | null {
  const got = samples.filter((x): x is number => x !== null);
  if (got.length < 2) return null;
  let sum = 0;
  for (let i = 1; i < got.length; i++) sum += Math.abs(got[i] - got[i - 1]);
  return Math.round(sum / (got.length - 1));
}

// ---- Settings › Appearance: the number beside the bars -------------------------------------

const SHOW_KEY = 'nb.netLatency';
const prefListeners = new Set<() => void>();

function readShow(): boolean {
  try {
    return localStorage.getItem(SHOW_KEY) !== 'off';
  } catch {
    return true;
  }
}

let show = readShow();

export function setLatencyShown(on: boolean): void {
  show = on;
  try {
    if (on) localStorage.removeItem(SHOW_KEY);
    else localStorage.setItem(SHOW_KEY, 'off');
  } catch {
    /* storage blocked: this page keeps it */
  }
  prefListeners.forEach((l) => l());
}

export function useLatencyShown(): boolean {
  return useSyncExternalStore(
    (fn) => {
      prefListeners.add(fn);
      return () => prefListeners.delete(fn);
    },
    () => show,
  );
}
