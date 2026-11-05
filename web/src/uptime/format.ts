import type { Monitor, MonitorStatus } from './api';

export const STATUS_LABEL: Record<MonitorStatus, string> = { up: 'Up', down: 'Down', pending: 'Pending', paused: 'Paused', waiting: 'Waiting' };
export const STATUS_DOT: Record<MonitorStatus, string> = { up: 'success', down: 'danger', pending: 'warning', paused: 'grey', waiting: 'grey' };
export const STATUS_BADGE: Record<MonitorStatus, string> = { up: 'badge-success', down: 'badge-danger', pending: 'badge-warning', paused: '', waiting: '' };

/** What matters most comes first. */
const RANK: Record<MonitorStatus, number> = { down: 0, pending: 1, waiting: 2, up: 3, paused: 4 };
export const byUrgency = (a: Monitor, b: Monitor) => RANK[a.status] - RANK[b.status] || a.name.localeCompare(b.name);

/** 100 % only when it truly is: 99.996 must not round up to it. */
export function percent(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—';
  if (v >= 100) return '100%';
  return `${(Math.floor(v * 100) / 100).toFixed(2)}%`;
}

export function millis(v: number | null | undefined): string {
  if (v === null || v === undefined || v < 0) return '—';
  if (v < 1000) return `${Math.round(v)} ms`;
  return `${(v / 1000).toFixed(v < 10000 ? 2 : 1)} s`;
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
