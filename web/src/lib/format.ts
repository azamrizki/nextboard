// Small formatting helpers.

/** Go's zero time ("0001-01-01T00:00:00Z") or empty → null. */
export function parseTime(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() <= 1) return null;
  return d;
}

export function formatDateTime(iso: string | null | undefined): string {
  const d = parseTime(iso);
  if (!d) return '—';
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export function timeAgo(iso: string | null | undefined, now = Date.now()): string {
  const d = parseTime(iso);
  if (!d) return 'never';
  const s = Math.round((now - d.getTime()) / 1000);
  if (s < 10) return 'just now';
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  const days = Math.round(h / 24);
  if (days < 30) return `${days} d ago`;
  return formatDateTime(iso);
}

/** timeAgo for tight spots: an old date without its time and, within this year, its year. */
export function timeAgoShort(iso: string | null | undefined, now = Date.now()): string {
  const d = parseTime(iso);
  if (!d) return 'never';
  if (now - d.getTime() < 30 * 24 * 3_600_000) return timeAgo(iso, now);
  const thisYear = d.getFullYear() === new Date(now).getFullYear();
  return d.toLocaleDateString(undefined, thisYear ? { month: 'short', day: 'numeric' } : { month: 'short', year: 'numeric' });
}

/** mm:ss countdown text. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

/** Safe localStorage access (may throw in private mode / blocked storage). */
export const prefs = {
  get(key: string): string | null {
    try {
      return window.localStorage.getItem(`nb.${key}`);
    } catch {
      return null;
    }
  },
  set(key: string, value: string) {
    try {
      window.localStorage.setItem(`nb.${key}`, value);
    } catch {
      /* ignore */
    }
  },
};
