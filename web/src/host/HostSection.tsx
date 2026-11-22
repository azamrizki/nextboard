// This PC on the Machines page (docs/API.md, Host resources): its card's gauges, and above the
// machines the details with the last hour.

import { useState } from 'react';
import { SegmentedControl } from '../components/form';
import { HostChart, HostPanel, METRIC_LABEL, tempLevel, useThresholds, type HostMetric } from './HostPanel';
import { levelOf, memPercent, useHost, useHostHistory } from './store';

function Gauge({ value, label, unit = '%', level = 'ok' }: { value: number | null; label: string; unit?: string; level?: string }) {
  const r = 25;
  const len = 2 * Math.PI * r * 0.75;
  const v = value == null ? 0 : Math.max(0, Math.min(100, value));
  return (
    <div className={`host-gauge lvl-${level}`}>
      <svg viewBox="0 0 62 62" aria-hidden="true">
        <circle cx="31" cy="33" r={r} className="host-gauge-track" strokeDasharray={`${len} 999`} transform="rotate(135 31 33)" />
        {value != null && <circle cx="31" cy="33" r={r} className="host-gauge-value" strokeDasharray={`${(len * v) / 100} 999`} transform="rotate(135 31 33)" />}
        <text x="31" y="38" textAnchor="middle" className="host-gauge-text">
          {value == null ? '—' : `${Math.round(value)}${unit}`}
        </text>
      </svg>
      <span>{label}</span>
    </div>
  );
}

/** The This PC card's gauges: CPU, memory, GPU and the hottest sensor, live. */
export function HostGauges() {
  const s = useHost();
  const th = useThresholds();
  if (!s?.supported) return null;
  const mem = memPercent(s);
  const gpu = s.gpus?.find((g) => g.busy != null)?.busy ?? null;
  const hot = s.hottest;
  return (
    <div className="host-gauges" role="group" aria-label="This PC now">
      <Gauge label="CPU" value={s.cpu.percent} />
      <Gauge label="Memory" value={mem} level={mem >= th.hotMemPct ? 'hot' : mem >= th.warnMemPct ? 'warn' : 'ok'} />
      <Gauge label="GPU" value={gpu} />
      <Gauge label={hot ? `Hottest · ${hot.label}` : 'Hottest'} value={hot?.c ?? null} unit="°" level={hot ? tempLevel(hot.c, th) : 'ok'} />
    </div>
  );
}

/** The details and the last hour, above the machines. */
export function HostSection() {
  const s = useHost();
  const history = useHostHistory();
  const [metric, setMetric] = useState<HostMetric>('cpu');
  if (s && !s.supported) return null;
  const metrics: HostMetric[] = ['cpu', 'mem', ...(history.some((p) => p.gpu != null) ? (['gpu'] as const) : []), ...(history.some((p) => p.temp != null) ? (['temp'] as const) : [])];
  return (
    <section className={`host-section lvl-${levelOf(s)}`} aria-label="This PC's resources">
      <div className="card">
        <HostPanel consoles={6} machinesLink={false} />
      </div>
      <div className="card">
        <div className="host-history-head">
          <h3>Last hour</h3>
          <SegmentedControl<HostMetric> size="sm" label="Show" value={metrics.includes(metric) ? metric : 'cpu'} onChange={setMetric} options={metrics.map((m) => ({ value: m, label: METRIC_LABEL[m] }))} />
        </div>
        <HostChart metric={metrics.includes(metric) ? metric : 'cpu'} />
      </div>
    </section>
  );
}
