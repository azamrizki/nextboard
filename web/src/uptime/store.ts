// A project's monitors, kept current by the hub's events. Every Uptime card of a project
// shows the same list.

import { useEffect, useSyncExternalStore } from 'react';
import { errorMessage } from '../api';
import { boardPath, lastWorkspace, navigate } from '../lib/router';
import { store } from '../store';
import { uptimeApi, type Monitor, type UptimeSettings } from './api';

const RECENT = 60;

export interface UptimeState {
  ready: boolean;
  error: string;
  monitors: Monitor[];
  settings: UptimeSettings;
}

const EMPTY: UptimeState = { ready: false, error: '', monitors: [], settings: {} };
const states = new Map<string, UptimeState>();
const listeners = new Set<() => void>();
let listening = false;

function set(projectId: string, patch: Partial<UptimeState>) {
  states.set(projectId, { ...(states.get(projectId) ?? EMPTY), ...patch });
  listeners.forEach((fn) => fn());
}

function change(projectId: string, fn: (list: Monitor[]) => Monitor[]) {
  const s = states.get(projectId);
  if (s) set(projectId, { monitors: fn(s.monitors) });
}

async function load(projectId: string) {
  try {
    const r = await uptimeApi.list(projectId);
    set(projectId, { ready: true, error: '', monitors: r.items ?? [], settings: r.settings ?? {} });
  } catch (err) {
    set(projectId, { ready: true, error: errorMessage(err) });
  }
}

function listen() {
  if (listening) return;
  listening = true;
  store.onEvent((ev) => {
    switch (ev.type) {
      case 'snapshot': // back after a lost connection: events were missed
        states.forEach((_, id) => void load(id));
        break;
      case 'uptimeBeat':
        change(ev.projectId, (list) =>
          list.map((m) => (m.id === ev.monitorId ? { ...ev.monitor, recent: [...(m.recent ?? []), ev.beat].slice(-RECENT) } : m)),
        );
        break;
      case 'uptimeMonitor':
        change(ev.monitor.projectId, (list) => (list.some((m) => m.id === ev.monitor.id) ? list.map((m) => (m.id === ev.monitor.id ? ev.monitor : m)) : [...list, ev.monitor]));
        break;
      case 'uptimeRemoved':
        change(ev.projectId, (list) => list.filter((m) => m.id !== ev.id));
        break;
    }
  });
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export function useUptime(projectId: string): UptimeState {
  useEffect(() => {
    listen();
    if (!states.has(projectId)) void load(projectId);
  }, [projectId]);
  return useSyncExternalStore(subscribe, () => states.get(projectId) ?? EMPTY);
}

export function setUptimeSettings(projectId: string, settings: UptimeSettings) {
  set(projectId, { settings });
}

/** Keep what the hub answered, unless an event brought a newer check while it was on its way. */
export function putMonitor(m: Monitor) {
  change(m.projectId, (list) => {
    const old = list.find((x) => x.id === m.id);
    if (!old) return [...list, m];
    const stale = old.lastCheck && (!m.lastCheck || new Date(m.lastCheck) < new Date(old.lastCheck));
    return stale ? list : list.map((x) => (x.id === m.id ? m : x));
  });
}

// ---- from a notification to the monitor ----

let wanted: { monitorId: string } | null = null;
const wantedListeners = new Set<() => void>();

/** The monitor a card should show now (a notification was clicked). */
export function useWantedMonitor() {
  return useSyncExternalStore(
    (fn) => {
      wantedListeners.add(fn);
      return () => wantedListeners.delete(fn);
    },
    () => wanted,
  );
}

export function clearWantedMonitor() {
  wanted = null;
  wantedListeners.forEach((fn) => fn());
}

export function revealMonitor(projectId: string, monitorId: string) {
  wanted = { monitorId };
  wantedListeners.forEach((fn) => fn());
  if (!location.pathname.startsWith(`/p/${projectId}/`)) navigate(boardPath(projectId, lastWorkspace(projectId) ?? undefined));
}
