// Token history (F4, docs/API-studio.md §10.3): tokens per day by CLI from the CLIs' own
// local transcripts (Claude projects, Codex sessions; every login profile), last 30 days.
// Stacked columns with a fixed colour per CLI (validated categorical palette, tokens
// --viz-*), legend, per-day tooltip on hover and focus, and a table view. Not a bill.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { useMachines } from '../store';
import { usageApi } from '../studio/api';
import type { TokenChart as Chart, TokenPoint } from '../studio/types';
import { compact, dayLabel, niceMax } from './format';

type Mode = 'all' | 'io' | 'output';
const MODE_LABEL: Record<Mode, string> = { all: 'All tokens', io: 'Input + output', output: 'Output only' };

/** Colour follows the CLI, never its rank (fixed slots). */
const SLOT: Record<string, number> = { claude: 1, codex: 2, gemini: 3 };
const slotOf = (agent: string) => SLOT[agent] ?? 4;
const AGENT_NAME: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', gemini: 'Gemini' };
const nameOf = (a: string) => AGENT_NAME[a] ?? a;

function value(p: TokenPoint, m: Mode): number {
  if (m === 'output') return p.output;
  if (m === 'io') return p.input + p.output;
  return p.input + p.output + p.cacheRead + p.cacheWrite;
}

export default function TokenChart({ machineId, onClose }: { machineId: string; onClose: () => void }) {
  const machines = useMachines();
  const [scope, setScope] = useState<'all' | 'this'>('all');
  const [data, setData] = useState<Chart | null>(null);
  const [error, setError] = useState('');
  const [mode, setMode] = useState<Mode>('all');
  const [table, setTable] = useState(false);
  const [range, setRange] = useState<'days30' | 'allTime'>('days30');

  useEffect(() => {
    let stop = false;
    setData(null);
    setError('');
    usageApi
      .tokens(30, scope === 'this' ? machineId : '')
      .then((d) => !stop && setData(d))
      .catch((err) => !stop && setError(errorMessage(err)));
    return () => {
      stop = true;
    };
  }, [machineId, scope]);

  const days = useMemo(() => {
    if (!data) return [];
    const out: string[] = [];
    const start = new Date(`${data.from}T12:00:00`);
    const end = new Date(`${data.to}T12:00:00`);
    for (let d = start; d <= end; d = new Date(d.getTime() + 86_400_000)) out.push(d.toISOString().slice(0, 10));
    return out;
  }, [data]);
  const agents = useMemo(() => [...new Set((data?.series ?? []).map((p) => p.agent))].sort((a, b) => slotOf(a) - slotOf(b) || a.localeCompare(b)), [data]);
  const byDay = useMemo(() => {
    const m = new Map<string, Map<string, TokenPoint>>();
    for (const p of data?.series ?? []) {
      if (!m.has(p.date)) m.set(p.date, new Map());
      m.get(p.date)!.set(p.agent, p);
    }
    return m;
  }, [data]);
  const total30 = useMemo(() => (data?.byAgent ?? []).reduce((s, a) => s + a.days30.total, 0), [data]);

  return (
    <Modal title="Token history" icon="chartBar" size="lg" className="tokens-modal" onClose={onClose}>
      <div className="tokens-filters">
        <SegmentedControl<Mode> value={mode} onChange={setMode} size="sm" label="Count" options={(['all', 'io', 'output'] as Mode[]).map((m) => ({ value: m, label: MODE_LABEL[m] }))} />
        <span className="spacer" />
        {machines.length > 1 && (
          <SegmentedControl<'all' | 'this'>
            value={scope}
            onChange={setScope}
            size="sm"
            label="Machines"
            options={[
              { value: 'all', label: 'All machines' },
              { value: 'this', label: machines.find((m) => m.id === machineId)?.name ?? 'This machine' },
            ]}
          />
        )}
        <Button size="sm" variant="ghost" icon={table ? 'chartBar' : 'rows'} aria-pressed={table} onClick={() => setTable((t) => !t)}>
          {table ? 'Chart' : 'Table'}
        </Button>
      </div>
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {!data && !error && (
        <p className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Reading local transcripts…
        </p>
      )}
      {data && (
        <>
          <div className="stat-tiles">
            <div className="stat-tile">
              <span className="stat-label">Last 30 days</span>
              <span className="stat-value tabular">{compact(total30)}</span>
              <span className="stat-sub">tokens, all CLIs</span>
            </div>
            <div className="stat-tile">
              <span className="stat-label">Busiest day</span>
              <span className="stat-value tabular">{data.busiestDay?.total ? compact(data.busiestDay.total) : '—'}</span>
              <span className="stat-sub">{data.busiestDay?.total ? dayLabel(data.busiestDay.date) : 'no activity'}</span>
            </div>
            <div className="stat-tile">
              <span className="stat-label">Top CLI</span>
              <span className="stat-value">{data.top ? nameOf(data.top) : '—'}</span>
              <span className="stat-sub">by tokens in 30 days</span>
            </div>
          </div>
          {agents.length > 1 && (
            <ul className="plain viz-legend" aria-label="Legend">
              {agents.map((a) => (
                <li key={a}>
                  <span className={`viz-key viz-${slotOf(a)}`} aria-hidden="true" />
                  {nameOf(a)}
                </li>
              ))}
            </ul>
          )}
          {table ? (
            <div className="tokens-table-wrap nowheel">
              <table className="tokens-table">
                <thead>
                  <tr>
                    <th scope="col">Day</th>
                    {agents.map((a) => (
                      <th key={a} scope="col" className="num">
                        {nameOf(a)}
                      </th>
                    ))}
                    <th scope="col" className="num">
                      Total
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {[...days].reverse().map((d) => {
                    const row = byDay.get(d);
                    const vals = agents.map((a) => (row?.get(a) ? value(row.get(a)!, mode) : 0));
                    return (
                      <tr key={d}>
                        <th scope="row">{dayLabel(d)}</th>
                        {vals.map((v, i) => (
                          <td key={agents[i]} className="num tabular">
                            {v.toLocaleString()}
                          </td>
                        ))}
                        <td className="num tabular">{vals.reduce((s, v) => s + v, 0).toLocaleString()}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <Columns days={days} agents={agents} byDay={byDay} mode={mode} />
          )}
          <div className="tokens-by-agent">
            <div className="inline-controls">
              <h3 className="section-title">By CLI</h3>
              <span className="spacer" />
              <SegmentedControl<'days30' | 'allTime'>
                value={range}
                onChange={setRange}
                size="sm"
                label="Range"
                options={[
                  { value: 'days30', label: '30 days' },
                  { value: 'allTime', label: 'All time' },
                ]}
              />
            </div>
            <ul className="plain by-agent">
              {(data.byAgent ?? []).map((a) => (
                <li key={a.agent}>
                  <span className={`viz-key viz-${slotOf(a.agent)}`} aria-hidden="true" />
                  <span>{nameOf(a.agent)}</span>
                  <span className="spacer" />
                  <span className="tabular">{(range === 'days30' ? a.days30.total : a.allTime.total).toLocaleString()} tokens</span>
                  {range === 'days30' && <span className="tabular text-3 fs-xs">{compact(a.days30.output)} output</span>}
                </li>
              ))}
              {!data.byAgent?.length && <li className="text-3">No transcripts found yet.</li>}
            </ul>
          </div>
          <p className="fs-xs text-3">
            Counted from the CLIs’ own transcripts on your machines{data.partial ? ' (partly: the scan ran out of time, the rest comes next time)' : ''}. This is not a bill —
            your provider’s dashboard is the source of truth.
          </p>
        </>
      )}
    </Modal>
  );
}

function Columns({ days, agents, byDay, mode }: { days: string[]; agents: string[]; byDay: Map<string, Map<string, TokenPoint>>; mode: Mode }) {
  const box = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(640);
  const [hover, setHover] = useState<{ i: number; x: number } | null>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  const H = 220;
  const pad = { l: 44, r: 8, t: 10, b: 24 };
  const totals = days.map((d) => agents.reduce((s, a) => s + (byDay.get(d)?.get(a) ? value(byDay.get(d)!.get(a)!, mode) : 0), 0));
  const max = niceMax(Math.max(0, ...totals));
  const band = (w - pad.l - pad.r) / Math.max(1, days.length);
  const bw = Math.max(2, Math.min(24, band - 4));
  const y = (v: number) => pad.t + (H - pad.t - pad.b) * (1 - v / max);
  const ticks = [0, max / 2, max];
  const GAP = 2;
  return (
    <div className="viz-wrap" ref={box} onPointerLeave={() => setHover(null)}>
      <svg width={w} height={H} className="viz" role="img" aria-label={`Tokens per day, ${MODE_LABEL[mode].toLowerCase()}, last ${days.length} days`}>
        {ticks.map((t) => (
          <g key={t}>
            <line className="viz-grid" x1={pad.l} x2={w - pad.r} y1={y(t)} y2={y(t)} />
            <text className="viz-tick" x={pad.l - 6} y={y(t)} textAnchor="end" dominantBaseline="middle">
              {compact(t)}
            </text>
          </g>
        ))}
        {days.map((d, i) => {
          const x = pad.l + i * band + (band - bw) / 2;
          let acc = 0;
          const segs = agents
            .map((a) => ({ a, v: byDay.get(d)?.get(a) ? value(byDay.get(d)!.get(a)!, mode) : 0 }))
            .filter((s) => s.v > 0);
          return (
            <g
              key={d}
              className={`viz-col ${hover?.i === i ? 'is-hover' : ''}`}
              tabIndex={0}
              aria-label={`${dayLabel(d)}: ${totals[i].toLocaleString()} tokens`}
              onPointerEnter={() => setHover({ i, x: x + bw / 2 })}
              onFocus={() => setHover({ i, x: x + bw / 2 })}
              onBlur={() => setHover(null)}
            >
              <rect className="viz-hit" x={pad.l + i * band} y={pad.t} width={band} height={H - pad.t - pad.b} />
              {segs.map((s, k) => {
                const y0 = y(acc);
                acc += s.v;
                const y1 = y(acc);
                const h = Math.max(0, y0 - y1 - (k > 0 ? GAP : 0));
                const top = k === segs.length - 1;
                const r = top ? Math.min(4, h / 2, bw / 2) : 0;
                const yy = y1;
                // 4px rounded data end on the top segment only, square at the baseline
                const path = `M${x},${yy + h} V${yy + r} Q${x},${yy} ${x + r},${yy} H${x + bw - r} Q${x + bw},${yy} ${x + bw},${yy + r} V${yy + h} Z`;
                return h > 0 ? <path key={s.a} className={`viz-fill viz-${slotOf(s.a)}`} d={path} /> : null;
              })}
            </g>
          );
        })}
        <line className="viz-axis" x1={pad.l} x2={w - pad.r} y1={y(0)} y2={y(0)} />
        {days.map((d, i) =>
          i % Math.ceil(days.length / Math.max(2, Math.floor(w / 90))) === 0 ? (
            <text key={d} className="viz-tick" x={pad.l + i * band + band / 2} y={H - 6} textAnchor="middle">
              {dayLabel(d)}
            </text>
          ) : null,
        )}
      </svg>
      {hover && (
        <div className="viz-tip" style={{ left: Math.min(Math.max(hover.x, 90), w - 90) }} role="status">
          <strong className="tabular">{totals[hover.i].toLocaleString()}</strong>
          <span className="text-3"> tokens · {dayLabel(days[hover.i])}</span>
          {agents.map((a) => {
            const p = byDay.get(days[hover.i])?.get(a);
            return (
              <div key={a} className="viz-tip-row">
                <span className={`viz-line viz-${slotOf(a)}`} aria-hidden="true" />
                <strong className="tabular">{(p ? value(p, mode) : 0).toLocaleString()}</strong>
                <span className="text-3">{nameOf(a)}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
