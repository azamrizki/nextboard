// Response time over a range: one line, one axis, a band where checks failed. Hover or the
// arrow keys read a point; the same numbers are one click away as a table.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { niceMax } from '../usage/format';
import { uptimeApi, type Series, type SeriesPoint, type SeriesRange } from './api';
import { dateTime, millis, percent } from './format';

const RANGES: { value: SeriesRange; label: string }[] = [
  { value: '1h', label: '1 h' },
  { value: '24h', label: '24 h' },
  { value: '7d', label: '7 d' },
  { value: '30d', label: '30 d' },
];
const RANGE_NAME: Record<SeriesRange, string> = { '1h': 'last hour', '24h': 'last 24 hours', '7d': 'last 7 days', '30d': 'last 30 days', '1y': 'last year' };
/** Pushes can come fast: a chart need not fetch at every one. */
const REFRESH_MS = 5_000;
const H = 200;
const PAD = { l: 52, r: 12, t: 10, b: 24 };

function tickLabel(t: number, range: SeriesRange): string {
  const d = new Date(t);
  if (range === '1h' || range === '24h') return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return d.toLocaleDateString([], { day: 'numeric', month: 'short' });
}

const pointSummary = (p: SeriesPoint) => {
  const parts = [`${p.up} up`];
  if (p.down) parts.push(`${p.down} down`);
  if (p.pending) parts.push(`${p.pending} pending`);
  return parts.join(', ');
};

export function ResponseChart({ monitorId, lastCheck }: { monitorId: string; lastCheck?: string }) {
  const [range, setRange] = useState<SeriesRange>('24h');
  const [data, setData] = useState<Series | null>(null);
  const [error, setError] = useState('');
  const [table, setTable] = useState(false);
  const fetched = useRef({ key: '', at: 0 });

  useEffect(() => {
    const key = `${monitorId}:${range}`;
    const same = fetched.current.key === key;
    if (same && Date.now() - fetched.current.at < REFRESH_MS) return;
    if (!same) setData(null);
    fetched.current = { key, at: Date.now() };
    // Not undone by the next beat, which would only find this one still under way:
    // only an answer for another monitor or range is out of date.
    uptimeApi
      .history(monitorId, range)
      .then((d) => {
        if (fetched.current.key !== key) return;
        setData(d);
        setError('');
      })
      .catch((err) => fetched.current.key === key && setError(errorMessage(err)));
  }, [monitorId, range, lastCheck]);

  const stats = data?.stats;
  return (
    <section className="up-chart" aria-label="Response time">
      <div className="up-chart-head">
        <h4 className="up-section-title">Response time</h4>
        {stats && stats.checks > 0 && (
          <span className="up-chart-sum tabular">
            {percent(stats.uptime)} uptime · avg {millis(stats.avgMs)} · {stats.checks === 1 ? '1 check' : `${stats.checks.toLocaleString()} checks`}
          </span>
        )}
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon={table ? 'chartBar' : 'table'} aria-label={table ? 'Show as chart' : 'Show as table'} title={table ? 'Show as chart' : 'Show as table'} onClick={() => setTable(!table)} />
        <SegmentedControl<SeriesRange> size="sm" label="Range" value={range} onChange={setRange} options={RANGES} />
      </div>
      {error ? (
        <div className="alert" role="alert">
          {error}
        </div>
      ) : !data ? (
        <div className="up-chart-empty">Loading…</div>
      ) : !data.points.length ? (
        <div className="up-chart-empty">No checks in the {RANGE_NAME[range]}.</div>
      ) : table ? (
        <PointsTable series={data} />
      ) : (
        <Plot series={data} />
      )}
    </section>
  );
}

function PointsTable({ series }: { series: Series }) {
  return (
    <div className="tokens-table-wrap nowheel up-table-wrap">
      <table className="tokens-table">
        <thead>
          <tr>
            <th scope="col">From</th>
            <th scope="col" className="num">
              Average
            </th>
            <th scope="col" className="num">
              Slowest
            </th>
            <th scope="col" className="num">
              Up
            </th>
            <th scope="col" className="num">
              Down
            </th>
          </tr>
        </thead>
        <tbody>
          {[...series.points].reverse().map((p) => (
            <tr key={p.t}>
              <th scope="row">{dateTime(p.t)}</th>
              <td className="num tabular">{millis(p.avgMs)}</td>
              <td className="num tabular">{p.avgMs >= 0 ? millis(p.maxMs) : '—'}</td>
              <td className="num tabular">{p.up.toLocaleString()}</td>
              <td className="num tabular">{p.down.toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Plot({ series }: { series: Series }) {
  const box = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(600);
  const [hover, setHover] = useState<number | null>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const { points, from, to, stepSec, range } = series;
  const stepMs = stepSec * 1000;
  const max = useMemo(() => niceMax(Math.max(1, ...points.map((p) => p.avgMs))), [points]);
  const innerW = Math.max(10, w - PAD.l - PAD.r);
  const x = (t: number) => PAD.l + ((t - from) / Math.max(1, to - from)) * innerW;
  const y = (v: number) => PAD.t + (H - PAD.t - PAD.b) * (1 - v / max);
  const bottom = y(0);

  // A stretch without checks is a gap in the line, not a slope across it.
  const { line, area } = useMemo(() => {
    let l = '';
    let a = '';
    let start: number | null = null;
    let last: SeriesPoint | null = null;
    const close = () => {
      if (start !== null && last) a += `L${x(last.t + stepMs / 2)},${bottom}L${x(start + stepMs / 2)},${bottom}Z`;
    };
    for (const p of points) {
      if (p.avgMs < 0) continue;
      const cx = x(p.t + stepMs / 2);
      const cy = y(p.avgMs);
      if (!last || p.t - last.t > stepMs * 2) {
        close();
        l += `M${cx},${cy}`;
        a += `M${cx},${cy}`;
        start = p.t;
      } else {
        l += `L${cx},${cy}`;
        a += `L${cx},${cy}`;
      }
      last = p;
    }
    close();
    return { line: l, area: a };
    // x and y follow from these
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points, w, max, from, to]);

  const bandW = Math.max(2, (stepMs / Math.max(1, to - from)) * innerW);
  const tickCount = Math.max(2, Math.min(6, Math.floor(innerW / 110)));
  const ticks = Array.from({ length: tickCount }, (_, i) => from + ((to - from) * (i + 0.5)) / tickCount);
  const point = hover === null ? null : points[hover];
  const measured = points.filter((p) => p.avgMs >= 0);
  const newest = measured[measured.length - 1];

  const pick = (clientX: number, el: Element) => {
    // as a share of the width, which the board's zoom does not change
    const r = el.getBoundingClientRect();
    const t = from + (((clientX - r.left) / r.width) * w - PAD.l) / innerW * (to - from);
    let best = 0;
    for (let i = 1; i < points.length; i++) if (Math.abs(points[i].t + stepMs / 2 - t) < Math.abs(points[best].t + stepMs / 2 - t)) best = i;
    setHover(best);
  };

  return (
    <div className="viz-wrap up-plot" ref={box} onPointerLeave={() => setHover(null)}>
      <svg
        width={w}
        height={H}
        className="viz"
        role="img"
        aria-label={`Response time, ${RANGE_NAME[range]}: average ${millis(series.stats.avgMs)}, slowest ${millis(series.stats.maxMs)}`}
        tabIndex={0}
        onPointerMove={(e) => pick(e.clientX, e.currentTarget)}
        onFocus={() => setHover((h) => h ?? points.length - 1)}
        onBlur={() => setHover(null)}
        onKeyDown={(e) => {
          if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
          e.preventDefault();
          e.stopPropagation();
          setHover((h) => Math.min(points.length - 1, Math.max(0, (h ?? points.length - 1) + (e.key === 'ArrowLeft' ? -1 : 1))));
        }}
      >
        {[0, max / 2, max].map((t) => (
          <g key={t}>
            <line className="viz-grid" x1={PAD.l} x2={w - PAD.r} y1={y(t)} y2={y(t)} />
            <text className="viz-tick" x={PAD.l - 6} y={y(t)} textAnchor="end" dominantBaseline="middle">
              {millis(t)}
            </text>
          </g>
        ))}
        {points.map((p) => (p.down > 0 ? <rect key={p.t} className="up-band" x={x(p.t)} y={PAD.t} width={bandW} height={bottom - PAD.t} /> : null))}
        <path className="up-area" d={area} />
        <path className="up-line" d={line} />
        {/* the latest value; with a single check, also all there is to see */}
        {newest && <circle className="up-dot" cx={x(newest.t + stepMs / 2)} cy={y(newest.avgMs)} r={4} />}
        <line className="viz-axis" x1={PAD.l} x2={w - PAD.r} y1={bottom} y2={bottom} />
        {ticks.map((t) => (
          <text key={t} className="viz-tick" x={x(t)} y={H - 6} textAnchor="middle">
            {tickLabel(t, range)}
          </text>
        ))}
        {point && (
          <g>
            <line className="up-cross" x1={x(point.t + stepMs / 2)} x2={x(point.t + stepMs / 2)} y1={PAD.t} y2={bottom} />
            {point.avgMs >= 0 && <circle className="up-dot" cx={x(point.t + stepMs / 2)} cy={y(point.avgMs)} r={4} />}
          </g>
        )}
      </svg>
      {point && (
        <div className="viz-tip" style={{ left: Math.min(Math.max(x(point.t + stepMs / 2), 110), w - 110) }} role="status">
          <strong className="tabular">{millis(point.avgMs)}</strong>
          <span className="text-3"> · {dateTime(point.t)}</span>
          {point.avgMs >= 0 && point.minMs !== point.maxMs && (
            <div className="text-3 tabular">
              {millis(point.minMs)} to {millis(point.maxMs)}
            </div>
          )}
          <div className="text-3 tabular">{pointSummary(point)}</div>
        </div>
      )}
    </div>
  );
}
