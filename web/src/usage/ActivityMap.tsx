// A year of activity, one square per day (F4 data, docs/API-studio.md §10.3). Colour steps are
// quartiles of the active days, so one huge day doesn't wash out all the others.

import { useMemo } from 'react';
import { compact, dayLabel } from './format';

const W = 388;
const LEFT = 28;
const TOP = 15;
const WEEKS = 53;
const PITCH = (W - LEFT) / WEEKS;
const CELL = PITCH - 1.2;
const MONTH_GAP = 3; // weeks between two month labels at least
const DAY = 86_400_000;

const toDate = (d: string) => new Date(`${d}T00:00:00Z`);
const toKey = (t: number) => new Date(t).toISOString().slice(0, 10);

interface Props {
  from: string;
  to: string;
  days: Map<string, number>;
}

export function ActivityMap({ from, to, days }: Props) {
  const { cells, months } = useMemo(() => {
    const end = toDate(to).getTime();
    const start = Math.max(toDate(from).getTime(), end - (WEEKS * 7 - 1) * DAY);
    const first = start - new Date(start).getUTCDay() * DAY; // back to its Sunday
    const values = [...days.values()].filter((v) => v > 0).sort((a, b) => a - b);
    const q = (p: number) => values[Math.floor((values.length - 1) * p)] ?? 0;
    const cuts = [q(0.25), q(0.5), q(0.75)];
    const cells: { key: string; x: number; y: number; level: number; value: number }[] = [];
    const months: { x: number; label: string }[] = [];
    let lastMonth = -1;
    let lastLabel = -MONTH_GAP;
    for (let t = first, i = 0; t <= end; t += DAY, i++) {
      const week = Math.floor(i / 7);
      if (t < start) continue;
      const key = toKey(t);
      const value = days.get(key) ?? 0;
      const level = value <= 0 ? 0 : value <= cuts[0] ? 1 : value <= cuts[1] ? 2 : value <= cuts[2] ? 3 : 4;
      cells.push({ key, x: LEFT + week * PITCH, y: TOP + (i % 7) * PITCH, level, value });
      const month = new Date(t).getUTCMonth();
      if (month !== lastMonth) {
        lastMonth = month;
        // a label starting in the last weeks wouldn't fit: its month shows soon enough
        if (week - lastLabel >= MONTH_GAP && week <= WEEKS - MONTH_GAP) {
          months.push({ x: LEFT + week * PITCH, label: new Date(t).toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' }) });
          lastLabel = week;
        }
      }
    }
    return { cells, months };
  }, [from, to, days]);
  const active = cells.filter((c) => c.level > 0).length;

  return (
    <svg className="activity-map" viewBox={`0 0 ${W} ${TOP + 7 * PITCH}`} role="img" aria-label={`Activity: ${active} active days in the last ${cells.length} days`}>
      {months.map((m) => (
        <text key={`${m.x}`} className="activity-label" x={m.x} y={9}>
          {m.label}
        </text>
      ))}
      {['Mon', 'Wed', 'Fri'].map((d, i) => (
        <text key={d} className="activity-label" x={0} y={TOP + (i * 2 + 1) * PITCH + CELL - 0.5}>
          {d}
        </text>
      ))}
      {cells.map((c) => (
        <rect key={c.key} className={`heat-${c.level}`} x={c.x} y={c.y} width={CELL} height={CELL} rx={1.2}>
          <title>{`${dayLabel(c.key)}: ${c.value ? `${compact(c.value)} tokens` : 'no activity'}`}</title>
        </rect>
      ))}
    </svg>
  );
}
