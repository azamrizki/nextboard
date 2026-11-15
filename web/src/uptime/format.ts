import { niceMax } from '../usage/format';
import type { Monitor, MonitorStatus } from './api';

export const STATUS_LABEL: Record<MonitorStatus, string> = { up: 'Up', down: 'Down', pending: 'Pending', paused: 'Paused', waiting: 'Waiting', maintenance: 'Maintenance' };
export const STATUS_DOT: Record<MonitorStatus, string> = { up: 'success', down: 'danger', pending: 'warning', paused: 'grey', waiting: 'grey', maintenance: 'info' };
export const STATUS_BADGE: Record<MonitorStatus, string> = { up: 'badge-success', down: 'badge-danger', pending: 'badge-warning', paused: '', waiting: '', maintenance: 'badge-info' };

/** What matters most comes first. */
const RANK: Record<MonitorStatus, number> = { down: 0, pending: 1, waiting: 2, maintenance: 3, up: 4, paused: 5 };
export const byUrgency = (a: Monitor, b: Monitor) => RANK[a.status] - RANK[b.status] || a.name.localeCompare(b.name);

/** 100 % only when it truly is: 99.996 must not round up to it. */
export function percent(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—';
  if (v >= 100) return '100%';
  return `${(Math.floor(v * 100) / 100).toFixed(2)}%`;
}

/** A time the hub measured. It counts whole milliseconds, cut off: 0 is under one. */
export function millis(v: number | null | undefined): string {
  if (v === null || v === undefined || v < 0) return '—';
  if (v < 1) return '<1 ms';
  if (v < 1000) return `${Math.round(v)} ms`;
  return `${(v / 1000).toFixed(v < 10000 ? 2 : 1)} s`;
}

const DAY_MS = 24 * 3600_000;

/**
 * The average response time of the last 24 hours, or null when no check in them was timed. The
 * hub sends 0 for both "under 1 ms" and "nothing timed" (a push without ping=, a monitor still
 * waiting): a timed check of the day tells them apart. However few checks there were.
 */
export function averageMs24h(m: Pick<Monitor, 'avgMs24h' | 'ms' | 'lastCheck' | 'recent'>, now = Date.now()): number | null {
  if (m.avgMs24h > 0) return m.avgMs24h;
  const since = now - DAY_MS;
  const timed = (m.recent ?? []).some((b) => b.ms >= 0 && b.t >= since) || (m.ms >= 0 && !!m.lastCheck && Date.parse(m.lastCheck) >= since);
  return timed ? 0 : null;
}

/** An axis label: as exact as the tick, so that no two read the same (0.5 ms, 12.5 ms, 1.25 s). */
export function msTick(v: number): string {
  const round = (x: number, digits: number) => String(Math.round(x * 10 ** digits) / 10 ** digits);
  return v < 1000 ? `${round(v, 2)} ms` : `${round(v / 1000, 3)} s`;
}

/** The response time axis for these times (-1, not timed, left out): its top and three ticks. */
export function msAxis(values: number[]): { max: number; ticks: number[] } {
  const max = niceMax(Math.max(1, ...values.filter((v) => v >= 0)));
  return { max, ticks: [0, max / 2, max] };
}

export function duration(sec: number): string {
  if (sec < 60) return `${Math.max(0, Math.round(sec))} s`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ${Math.floor((sec % 3600) / 60)} min`;
  return `${Math.floor(sec / 86400)} d ${Math.floor((sec % 86400) / 3600)} h`;
}

export function clock(t: number): string {
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function dateTime(t: number): string {
  return new Date(t).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}
