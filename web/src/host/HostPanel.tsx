// This PC's resources in detail (docs/API.md, Host resources): CPU, memory, GPUs, temperatures,
// disks and the consoles using the most. The header pill's popover, the phone's sheet, the
// This PC card and the Machines page show it; while it is open the hub reads every 2 s.

import { useEffectiveSettings } from '../agents/settings';
import { jumpToSession } from '../agents/jump';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { navigate } from '../lib/router';
import type { HostConsole, HostGPU, HostPoint, HostSnapshot, HostTemp } from '../lib/types';
import { bytes, memPercent, upFor, useHost, useHostDetails, useHostHistory } from './store';

export interface Thresholds {
  warnTempC: number;
  hotTempC: number;
  warnMemPct: number;
  hotMemPct: number;
}

export const DEFAULT_THRESHOLDS: Thresholds = { warnTempC: 80, hotTempC: 90, warnMemPct: 85, hotMemPct: 95 };

export function useThresholds(): Thresholds {
  return useEffectiveSettings().host ?? DEFAULT_THRESHOLDS;
}

export const tempLevel = (c: number, th: Thresholds) => (c >= th.hotTempC ? 'hot' : c >= th.warnTempC ? 'warn' : 'ok');
const memLevel = (pct: number, th: Thresholds) => (pct >= th.hotMemPct ? 'hot' : pct >= th.warnMemPct ? 'warn' : 'ok');

/** A small line of the last points of a series. */
export function Spark({ values, className }: { values: number[]; className?: string }) {
  if (values.length < 2) return <div className={`host-spark is-empty ${className ?? ''}`} aria-hidden="true" />;
  const w = 200;
  const h = 32;
  const pts = values.map((v, i) => `${((i * w) / (values.length - 1)).toFixed(1)},${(h - (Math.min(100, Math.max(0, v)) / 100) * h).toFixed(1)}`);
  return (
    <svg className={`host-spark ${className ?? ''}`} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden="true">
      <polygon points={`0,${h} ${pts.join(' ')} ${w},${h}`} className="host-spark-fill" />
      <polyline points={pts.join(' ')} className="host-spark-line" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function Bar({ parts, label }: { parts: { pct: number; tone: string }[]; label: string }) {
  return (
    <div className="host-bar" role="img" aria-label={label}>
      {parts.map((p, i) => (
        <i key={i} className={`tone-${p.tone}`} style={{ width: `${Math.max(0, Math.min(100, p.pct))}%` }} />
      ))}
    </div>
  );
}

function CpuTile({ s, history, compact }: { s: HostSnapshot; history: HostPoint[]; compact?: boolean }) {
  const pct = Math.round(s.cpu.percent);
  return (
    <section className="host-tile" aria-label="CPU">
      <span className="host-k">CPU</span>
      <span className="host-v tabular">
        {pct}%<small>load {s.cpu.load[0].toFixed(2)}</small>
      </span>
      <Spark values={history.slice(-40).map((p) => p.cpu)} />
      {!compact && !!s.cpu.cores?.length && (
        <div className="host-cores" aria-label={`Each thread: ${s.cpu.cores.map((c) => `${Math.round(c)}%`).join(', ')}`}>
          {s.cpu.cores.map((c, i) => (
            <i key={i} style={{ height: `${Math.max(8, c)}%` }} />
          ))}
        </div>
      )}
      <span className="host-d truncate" title={s.cpu.model}>
        {[shortModel(s.cpu.model), s.cpu.threads ? `${s.cpu.threads} threads` : ''].filter(Boolean).join(' · ')}
      </span>
    </section>
  );
}

/** "AMD Ryzen 5 3600XT 6-Core Processor" → "Ryzen 5 3600XT". */
function shortModel(m: string): string {
  return m
    .replace(/\((R|TM)\)/gi, '')
    .replace(/^(AMD|Intel)\s+/i, '')
    .replace(/\s+\d+-Core Processor$/i, '')
    .replace(/\s+CPU\s+@.*$/i, '')
    .trim();
}

function MemoryTile({ s, th }: { s: HostSnapshot; th: Thresholds }) {
  const m = s.memory;
  const pct = memPercent(s);
  const level = memLevel(pct, th);
  const cache = m.total ? Math.min(100 - pct, (m.cached * 100) / m.total) : 0;
  return (
    <section className="host-tile" aria-label="Memory">
      <span className="host-k">Memory</span>
      <span className={`host-v tabular lvl-${level}`}>
        {(m.used / 2 ** 30).toFixed(1)}
        <small>
          of {Math.round(m.total / 2 ** 30)} GiB · {Math.round(pct)}%
        </small>
      </span>
      <Bar label={`${Math.round(pct)}% used`} parts={[{ pct, tone: level === 'ok' ? 'info' : level }, { pct: cache, tone: 'dim' }]} />
      <span className="host-d">
        {bytes(m.available)} free, {bytes(m.cached)} of it as cache
      </span>
      {m.swapTotal > 0 && (
        <>
          <span className="host-k host-k-sub">Swap</span>
          <Bar label={`Swap ${Math.round((m.swapUsed * 100) / m.swapTotal)}% used`} parts={[{ pct: (m.swapUsed * 100) / m.swapTotal, tone: 'warn' }]} />
          <span className="host-d">
            {bytes(m.swapUsed)} of {bytes(m.swapTotal)}
          </span>
        </>
      )}
    </section>
  );
}

function GpuTile({ g, th }: { g: HostGPU; th: Thresholds }) {
  const facts = [
    g.tempC != null ? `${Math.round(g.tempC)} °C` : '',
    g.powerW != null ? `${Math.round(g.powerW)} W` : '',
    g.vramTotal ? `VRAM ${bytes(g.vramUsed ?? 0)} / ${bytes(g.vramTotal)}` : '',
  ].filter(Boolean);
  return (
    <section className="host-tile" aria-label={`GPU ${g.name}`}>
      <span className="host-k">GPU</span>
      <span className={`host-v tabular ${g.tempC != null ? `lvl-${tempLevel(g.tempC, th)}` : ''}`}>
        {g.busy != null ? `${Math.round(g.busy)}%` : '—'}
        <small className="truncate">{g.name}</small>
      </span>
      {g.busy != null && <Bar label={`${Math.round(g.busy)}% busy`} parts={[{ pct: g.busy, tone: 'info' }]} />}
      <span className="host-d truncate" title={g.busy == null ? 'Its driver does not tell how busy it is.' : undefined}>
        {facts.join(' · ') || g.driver}
      </span>
    </section>
  );
}

function TempsTile({ temps, th }: { temps: HostTemp[]; th: Thresholds }) {
  return (
    <section className="host-tile" aria-label="Temperatures">
      <span className="host-k">Temperatures</span>
      <div className="host-temps">
        {temps.slice(0, 5).map((t) => {
          const level = tempLevel(t.c, th);
          return (
            <div key={t.label + t.sensor} className="host-temp" title={t.sensor}>
              <span className="host-temp-n truncate">{t.label}</span>
              <Bar label={`${t.label} ${Math.round(t.c)} °C`} parts={[{ pct: t.c, tone: level === 'ok' ? 'ok' : level }]} />
              <span className={`host-temp-c tabular lvl-${level}`}>{Math.round(t.c)}°</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function ConsoleRow({ c, onPick }: { c: HostConsole; onPick?: () => void }) {
  const hub = c.sessionId === 'hub';
  const who = hub ? 'NextBoard' : c.handle ? `@${c.handle}` : c.name || 'Console';
  const where = hub ? 'the hub itself' : [c.agent && c.handle ? c.name : '', c.projectName].filter(Boolean).join(' · ');
  const body = (
    <>
      <span className="host-who-mark" aria-hidden="true">
        {hub ? <Icon name="server" /> : (c.handle || c.name || '?').charAt(0).toUpperCase()}
      </span>
      <span className="host-who truncate">
        <b>{who}</b> {where && <span>{where}</span>}
      </span>
      <span className="host-n tabular">{c.cpu < 1 && c.cpu > 0 ? '<1' : Math.round(c.cpu)}%</span>
      <span className="host-n tabular">{bytes(c.rss)}</span>
    </>
  );
  if (hub) return <div className="host-console">{body}</div>;
  return (
    <button
      type="button"
      className="host-console"
      title={`Show ${who}`}
      onClick={() => {
        onPick?.();
        jumpToSession(c.sessionId);
      }}
    >
      {body}
    </button>
  );
}

interface HostPanelProps {
  /** Fewer details: a card or a phone's sheet. */
  compact?: boolean;
  /** How many consoles to name (0: none). */
  consoles?: number;
  /** A link to the Machines page (not on it). */
  machinesLink?: boolean;
  /** Called when a link or console leaves the panel (a popover closes then). */
  onLeave?: () => void;
  /** full: title, facts and link; facts: no title (a sheet has its own); none: a card's own head. */
  head?: 'full' | 'facts' | 'none';
}

export function HostPanel({ compact, consoles = 3, machinesLink = true, onLeave, head = 'full' }: HostPanelProps) {
  useHostDetails(true);
  const s = useHost();
  const history = useHostHistory();
  const th = useThresholds();
  if (!s) {
    return (
      <div className="host-panel is-loading">
        <span className="spinner" aria-hidden="true" /> Reading This PC…
      </div>
    );
  }
  if (!s.supported) {
    return (
      <div className="host-panel">
        <p className="host-note">The hub reads CPU, memory and temperatures on Linux. This PC runs {s.os === 'darwin' ? 'macOS' : s.os}, which it cannot read yet.</p>
      </div>
    );
  }
  const temps = s.temps ?? [];
  const gpus = s.gpus ?? [];
  const list = (s.consoles ?? []).slice(0, consoles);
  const ago = Math.max(0, Math.round((Date.now() - Date.parse(s.at)) / 1000));
  return (
    <div className={`host-panel ${compact ? 'is-compact' : ''}`}>
      {head !== 'none' && (
        <div className="host-head">
          <div className="host-head-text">
            {head === 'full' && (
              <h3 className="host-title">
                <Icon name="monitor" /> This PC <span className="host-host">{s.hostname}</span>
              </h3>
            )}
            <span className="host-sub">
              {head === 'facts' && `${s.hostname} · `}
              Linux · {upFor(s.uptimeSec)} · read {ago < 2 ? 'just now' : `${ago} s ago`}
            </span>
          </div>
          {machinesLink && (
            <Button
              size="sm"
              onClick={() => {
                onLeave?.();
                navigate('/machines');
              }}
            >
              {compact ? 'History' : 'Open in Machines'}
            </Button>
          )}
        </div>
      )}
      {s.level !== 'ok' && s.why && (
        <p className={`host-why lvl-${s.level}`} role="status">
          <Icon name="alert" /> {s.level === 'hot' ? 'Running hot' : 'Running warm'}: {s.why}
        </p>
      )}
      <div className="host-tiles">
        <CpuTile s={s} history={history} compact={compact} />
        <MemoryTile s={s} th={th} />
        {gpus.map((g, i) => (
          <GpuTile key={g.name + i} g={g} th={th} />
        ))}
        {temps.length > 0 && <TempsTile temps={temps} th={th} />}
      </div>
      {(s.disks ?? []).map((d) => (
        <div key={d.path} className="host-disk">
          <span className="truncate">Disk · {d.path}</span>
          <Bar label={`${Math.round((d.used * 100) / d.total)}% used`} parts={[{ pct: d.total ? (d.used * 100) / d.total : 0, tone: d.used / d.total > 0.9 ? 'warn' : 'info' }]} />
          <span className="tabular">
            {bytes(d.used)} / {bytes(d.total)}
          </span>
        </div>
      ))}
      {consoles > 0 && (
        <div className="host-consoles">
          <div className="host-k host-consoles-head">
            <span>Consoles using the most</span>
            <span>CPU · Memory</span>
          </div>
          {list.length ? list.map((c) => <ConsoleRow key={c.sessionId} c={c} onPick={onLeave} />) : <p className="host-note">Counting…</p>}
        </div>
      )}
      {!compact && (
        <div className="host-foot">
          <span>
            Amber at {th.warnTempC} °C or {th.warnMemPct}% memory, red at {th.hotTempC} °C or {th.hotMemPct}%
          </span>
          <button
            type="button"
            className="link-btn"
            onClick={() => {
              onLeave?.();
              navigate('/settings/notifications');
            }}
          >
            Settings › Notifications
          </button>
        </div>
      )}
    </div>
  );
}

// ---- the last hour ----

export type HostMetric = 'cpu' | 'mem' | 'gpu' | 'temp';

const METRIC_LABEL: Record<HostMetric, string> = { cpu: 'CPU', mem: 'Memory', gpu: 'GPU', temp: 'Temperatures' };

/** The last hour of one series, as an area chart (0–100 % or °C). */
export function HostChart({ metric }: { metric: HostMetric }) {
  const history = useHostHistory();
  const values = history.map((p) => p[metric]);
  const pts = history.map((p, i) => ({ v: values[i], at: p.at })).filter((p): p is { v: number; at: number } => p.v != null);
  const w = 800;
  const h = 160;
  if (pts.length < 2) return <p className="host-note">The chart fills as the hub reads, one point every 15 s.</p>;
  const t0 = pts[0].at;
  const span = Math.max(60, pts[pts.length - 1].at - t0);
  const x = (at: number) => (((at - t0) / span) * w).toFixed(1);
  const y = (v: number) => (h - (Math.min(100, Math.max(0, v)) / 100) * h).toFixed(1);
  const line = pts.map((p) => `${x(p.at)},${y(p.v)}`).join(' ');
  const peak = pts.reduce((a, b) => (b.v > a.v ? b : a));
  const now = pts[pts.length - 1].v;
  const unit = metric === 'temp' ? ' °C' : '%';
  return (
    <figure className="host-chart">
      <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={`${METRIC_LABEL[metric]} over the last ${Math.round(span / 60)} minutes`}>
        {[25, 50, 75].map((g) => (
          <line key={g} x1="0" x2={w} y1={y(g)} y2={y(g)} className="host-chart-grid" />
        ))}
        <polygon points={`0,${h} ${line} ${x(pts[pts.length - 1].at)},${h}`} className="host-chart-fill" />
        <polyline points={line} className="host-chart-line" vectorEffect="non-scaling-stroke" />
      </svg>
      <figcaption className="host-chart-cap">
        <span>
          {METRIC_LABEL[metric]} {Math.round(now)}
          {unit} now · peak {Math.round(peak.v)}
          {unit} at {new Date(peak.at * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
        </span>
        <span>{Math.round(span / 60)} min</span>
      </figcaption>
    </figure>
  );
}

export { METRIC_LABEL };
