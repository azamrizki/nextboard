// Short forms of what the usage views print.

export function compact(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(n >= 1e10 ? 0 : 1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}K`;
  return String(n);
}

/** "claude-fable-5-1" → "Fable 5.1", "claude-opus-5-5-20260101" → "Opus 5.5"; others as they are. */
export function modelName(id: string): string {
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{6,})?$/.exec(id);
  if (!m) return id;
  return `${m[1][0].toUpperCase()}${m[1].slice(1)} ${m[2]}${m[3] ? `.${m[3]}` : ''}`;
}

/** 1621320 → "18d 18h 22m": the two or three largest units that say something. */
export function durationShort(sec: number): string {
  if (sec < 60) return sec > 0 ? `${sec}s` : '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return [d ? `${d}d` : '', d || h ? `${h}h` : '', `${m}m`].filter(Boolean).join(' ');
}

/** "Resets 4:09am (America/Cordoba)" → "resets 4:09am": for the pill, where the zone is noise. */
export function resetsShort(text: string): string {
  const t = text.replace(/\s*\([^)]*\)\s*$/, '').trim();
  return t ? t[0].toLowerCase() + t.slice(1) : '';
}

export const dayLabel = (d: string) => new Date(`${d}T12:00:00`).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });

/** The next round number at or above v: the top of a chart's axis. */
export function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}
