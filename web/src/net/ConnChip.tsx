// The header's connection chip (docs/API.md, Connection to the hub; DESIGN.md §3.4): signal bars
// and the round trip to the hub while live updates are connected, "Reconnecting…" while they are
// not. A click opens the details: the round trip over ten minutes, how long the live connection
// has held, the speed test and the route. A phone, or a tablet held upright, opens them as a
// sheet. Also the Code view's top bar.

import { useEffect, useRef, useState } from 'react';
import { Modal } from '../components/Modal';
import { Popover } from '../components/Popover';
import { usePortrait } from '../host/HostPill';
import { useLayout } from '../lib/layout';
import { useEventsConnected, useStoreReady } from '../store';
import { netBars, netJitter, netLevel, netMeasureNow, useLatencyShown, useNet, type NetLevel } from './latency';
import { runSpeedTest, stopSpeedTest, useSpeedTest, DOWN_BYTES, UP_BYTES, type SpeedResult } from './speedTest';

/** "2 h 14 m", "41 m", "30 s". */
export function heldFor(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} m`;
  return `${Math.floor(m / 60)} h ${m % 60} m`;
}

/** "2 min ago", "just now". */
function agoText(at: number, now: number): string {
  const m = Math.floor((now - at) / 60_000);
  if (m < 1) return 'just now';
  if (m < 60) return `${m} min ago`;
  return `${Math.floor(m / 60)} h ago`;
}

const MB = (n: number) => `${Math.round(n / 2 ** 20)} MB`;

const DOT: Record<NetLevel, string> = { good: 'dot-success', fair: 'dot-warning', poor: 'dot-danger', none: 'dot-grey' };

function Bars({ n }: { n: number }) {
  return (
    <span className="net-bars" aria-hidden="true">
      {[1, 2, 3, 4].map((i) => (
        <i key={i} className={i <= n ? 'on' : ''} />
      ))}
    </span>
  );
}

/** The round trips of the last ten minutes. */
function Spark({ samples }: { samples: readonly (number | null)[] }) {
  const got = samples.filter((x): x is number => x !== null);
  if (got.length < 2) return <span className="net-spark" aria-hidden="true" />;
  const max = Math.max(100, ...got);
  const step = 160 / (got.length - 1);
  const pts = got.map((v, i) => `${Math.round(i * step)},${Math.round(26 - (v / max) * 22)}`).join(' ');
  return (
    <svg className="net-spark" viewBox="0 0 160 28" preserveAspectRatio="none" aria-hidden="true">
      <polyline points={pts} />
    </svg>
  );
}

function protocolName(p: string): string {
  if (p === 'h2') return 'HTTP/2';
  if (p === 'h3') return 'HTTP/3';
  if (/^http\/1/i.test(p)) return p.toUpperCase();
  return '';
}

interface Route {
  proxied: boolean;
  protocol: string;
}

async function readRoute(): Promise<Route> {
  const url = new URL('/api/net', window.location.href).href;
  const res = await fetch(url, { cache: 'no-store', credentials: 'same-origin' });
  const body = (await res.json().catch(() => ({}))) as { proxied?: boolean };
  const entry = performance.getEntriesByName(url).pop() as PerformanceResourceTiming | undefined;
  return { proxied: !!body.proxied, protocol: protocolName(entry?.nextHopProtocol ?? '') };
}

function SpeedTile({ kind, result, running, progress }: { kind: 'Download' | 'Upload'; result: SpeedResult | null; running: boolean; progress: { loaded: number; total: number; mbps: number } | null }) {
  const now = Date.now();
  return (
    <div className="net-tile">
      <span className="net-k">{kind}</span>
      {running && progress ? (
        <>
          <span className="net-v tabular">
            {progress.mbps || '…'} <small>Mbps so far</small>
          </span>
          <span className="net-prog" role="progressbar" aria-label={`${kind} test`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round((progress.loaded / progress.total) * 100)}>
            <i style={{ width: `${(progress.loaded / progress.total) * 100}%` }} />
          </span>
          <span className="net-d tabular">
            {(progress.loaded / 2 ** 20).toFixed(1)} of {MB(progress.total)}
          </span>
        </>
      ) : result ? (
        <>
          <span className="net-v tabular">
            {result.mbps} <small>Mbps</small>
          </span>
          <span className="net-d tabular">
            {MB(result.bytes)} in {result.ms < 1000 ? `${result.ms} ms` : `${(result.ms / 1000).toFixed(1)} s`} · {agoText(result.at, now)}
          </span>
        </>
      ) : (
        <>
          <span className="net-v net-none">—</span>
          <span className="net-d">not tested yet</span>
        </>
      )}
    </div>
  );
}

/** The details, in the popover or the sheet. */
export function ConnPanel({ head = true }: { head?: boolean }) {
  const connected = useEventsConnected();
  const net = useNet();
  const speed = useSpeedTest();
  const [route, setRoute] = useState<Route | null>(null);
  const [, tick] = useState(0);

  useEffect(() => {
    let live = true;
    readRoute().then((r) => live && setRoute(r), () => undefined);
    const t = window.setInterval(() => tick((n) => n + 1), 5000);
    return () => {
      live = false;
      window.clearInterval(t);
    };
  }, []);

  const level = connected ? netLevel(net.rtt, net.samples) : 'none';
  const jitter = netJitter(net.samples);
  const running = speed.phase === 'down' || speed.phase === 'up';
  const https = window.location.protocol === 'https:';

  return (
    <div className="net-panel">
      {head && (
        <div className="net-head">
          <h3>Connection to the hub</h3>
          <span className="net-sub">This browser to {window.location.host}</span>
        </div>
      )}
      <div className="net-tiles">
        <div className="net-tile">
          <span className="net-k">
            <span className={`dot ${DOT[level]}`} aria-hidden="true" />
            Latency
          </span>
          <span className="net-v tabular">
            {connected && net.rtt != null ? net.rtt : '—'} <small>ms</small>
          </span>
          <Spark samples={net.samples} />
          <span className="net-d">{!connected ? 'not connected' : jitter != null ? `±${jitter} ms jitter` : 'measuring…'}</span>
        </div>
        <div className="net-tile">
          <span className="net-k">Live updates</span>
          <span className="net-v tabular">{connected && net.since ? heldFor(Date.now() - net.since) : 'Reconnecting…'}</span>
          <span className="net-d" title="Since this page opened">
            {net.reconnects} reconnect{net.reconnects === 1 ? '' : 's'} so far
          </span>
        </div>
        <SpeedTile kind="Download" result={speed.down} running={speed.phase === 'down'} progress={speed.progress} />
        <SpeedTile kind="Upload" result={speed.up} running={speed.phase === 'up'} progress={speed.progress} />
      </div>
      <dl className="net-kv">
        <dt>Address</dt>
        <dd>
          {window.location.host} · {https ? 'HTTPS' : 'HTTP'}
          {route?.protocol ? ` · ${route.protocol}` : ''}
        </dd>
        <dt>Route</dt>
        <dd>{route ? `${route.proxied ? 'through a proxy' : 'straight to the hub'}, live updates over WebSocket` : '…'}</dd>
      </dl>
      {speed.phase === 'error' && (
        <p className="net-error" role="alert">
          {speed.error}
        </p>
      )}
      <div className="net-foot">
        {running ? (
          <button type="button" className="btn btn-secondary btn-sm" onClick={stopSpeedTest}>
            Stop
          </button>
        ) : (
          <button type="button" className={`btn btn-sm ${speed.down ? 'btn-secondary' : 'btn-primary'}`} onClick={() => void runSpeedTest()}>
            {speed.down ? 'Test again' : 'Test speed'}
          </button>
        )}
        <span>{running ? 'Consoles keep working while it runs.' : `About ${MB(DOWN_BYTES)} down and ${MB(UP_BYTES)} up, only when you press it.`}</span>
      </div>
    </div>
  );
}

/** The chip in the header and the Code view's top bar. */
export function ConnChip() {
  const connected = useEventsConnected();
  const ready = useStoreReady();
  const net = useNet();
  const shown = useLatencyShown();
  const { layout } = useLayout();
  const upright = usePortrait();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);

  const level = connected ? netLevel(net.rtt, net.samples) : 'none';
  const measured = connected && shown && net.rtt != null;
  const label = !connected ? (ready ? 'Reconnecting…' : 'Connecting…') : measured ? `${net.rtt} ms` : 'Live';
  const title = !connected ? 'Live updates disconnected — reconnecting' : net.rtt != null ? `Live updates connected · ${net.rtt} ms round trip to the hub` : 'Live updates connected';
  const sheet = layout === 'phone' || (layout === 'tablet' && upright);
  const close = () => setOpen(false);

  return (
    <>
      <button
        ref={ref}
        type="button"
        className={`status conn conn-chip lvl-${level} ${connected ? 'is-live' : ''} ${open ? 'is-open' : ''}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`${title}. Open the connection details`}
        title={title}
        onClick={() => {
          if (!open) netMeasureNow();
          setOpen(!open);
        }}
      >
        {measured ? <Bars n={netBars(net.rtt, level)} /> : <span className={`dot ${connected ? 'dot-success' : 'dot-warning'}`} aria-hidden="true" />}
        <span className={`conn-text ${connected ? 'conn-label-live' : 'text-warning'} ${measured ? 'tabular' : ''}`}>{label}</span>
      </button>
      {open &&
        (sheet ? (
          <Modal title="Connection to the hub" icon="activity" onClose={close} size="md" className="net-sheet">
            <ConnPanel head={false} />
          </Modal>
        ) : (
          <Popover anchor={ref} onClose={close} label="Connection to the hub" className="net-popover" width={420}>
            <ConnPanel />
          </Popover>
        ))}
    </>
  );
}
