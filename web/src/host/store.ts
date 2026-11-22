// This PC's resources (docs/API.md, Host resources): the header's reading comes with the hub's
// "host" events (every 5 s while a page is open). A view of the details (the popover, the
// sheet, the This PC card, the Machines page) asks GET /api/host/resources every 2 s while
// it is open: the hub then reads faster and names the consoles using the most. One poll
// serves every open view.

import { useEffect, useSyncExternalStore } from 'react';
import { api } from '../api';
import type { HostLevel, HostPoint, HostSnapshot } from '../lib/types';
import { store } from '../store';

/** What the details ask every so often, and how long the history is. */
export const DETAILS_EVERY_MS = 2000;
const HISTORY_STEP_S = 15;
const HISTORY_LEN = 240;

interface HostState {
  snap: HostSnapshot | null;
  history: HostPoint[];
  /** The history came from the hub (else it is only what this page saw). */
  historyLoaded: boolean;
}

let state: HostState = { snap: null, history: [], historyLoaded: false };
const listeners = new Set<() => void>();

function set(patch: Partial<HostState>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

function subscribe(fn: () => void) {
  install();
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** A new reading: the latest, and a point of the history every 15 s. */
export function takeSnapshot(snap: HostSnapshot, keepConsoles = false) {
  const prev = state.snap;
  // an event has no consoles: the details' last ones stay while they look
  const next = !snap.consoles && keepConsoles && prev?.consoles ? { ...snap, consoles: prev.consoles } : snap;
  const at = Math.floor(Date.parse(snap.at) / 1000);
  const last = state.history[state.history.length - 1];
  let history = state.history;
  if (Number.isFinite(at) && (!last || at - last.at >= HISTORY_STEP_S)) {
    const gpu = snap.gpus?.find((g) => g.busy != null)?.busy ?? null;
    history = [...history, { at, cpu: snap.cpu.percent, mem: memPercent(snap), gpu, temp: snap.hottest?.c ?? null }].slice(-HISTORY_LEN);
  }
  set({ snap: next, history });
}

let installed = false;
function install() {
  if (installed) return;
  installed = true;
  store.onEvent((ev) => {
    if (ev.type === 'host') takeSnapshot(ev.host, watchers > 0);
  });
  // the first reading at once, not after the next event
  void api
    .hostResources()
    .then((r) => {
      if (!state.snap) takeSnapshot(r.host);
    })
    .catch(() => undefined);
}

export function useHost(): HostSnapshot | null {
  return useSyncExternalStore(subscribe, () => state.snap);
}

export function useHostHistory(): HostPoint[] {
  return useSyncExternalStore(subscribe, () => state.history);
}

// ---- the details' poll: one for every open view ----

let watchers = 0;
let timer = 0;

async function poll(withHistory: boolean) {
  if (document.hidden) return;
  try {
    const r = await api.hostResources(withHistory);
    if (r.history) set({ history: r.history.slice(-HISTORY_LEN), historyLoaded: true });
    takeSnapshot(r.host);
  } catch {
    /* the next one, or the events, will do */
  }
}

/** While on, the details stay fresh: the hub reads every 2 s and names the consoles. */
export function useHostDetails(on = true) {
  useEffect(() => {
    if (!on) return;
    install();
    watchers++;
    if (watchers === 1) {
      void poll(!state.historyLoaded);
      timer = window.setInterval(() => void poll(false), DETAILS_EVERY_MS);
    }
    return () => {
      watchers--;
      if (watchers === 0) window.clearInterval(timer);
    };
  }, [on]);
}

// ---- reading it ----

export function memPercent(s: HostSnapshot): number {
  return s.memory.total ? Math.round((s.memory.used * 1000) / s.memory.total) / 10 : 0;
}

export function levelOf(s: HostSnapshot | null): HostLevel {
  return s?.level === 'warn' || s?.level === 'hot' ? s.level : 'ok';
}

/** Bytes as people read them: 6.4 GiB, 430 MB. */
export function bytes(n: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** Up 13 days, up 5 h, up 12 min. */
export function upFor(sec: number): string {
  if (sec >= 2 * 86400) return `up ${Math.floor(sec / 86400)} days`;
  if (sec >= 3600) return `up ${Math.floor(sec / 3600)} h`;
  return `up ${Math.max(1, Math.floor(sec / 60))} min`;
}

/** Test helpers: what is read, and back to nothing. */
export const hostState = (): HostState => state;

export function resetHost() {
  state = { snap: null, history: [], historyLoaded: false };
}
