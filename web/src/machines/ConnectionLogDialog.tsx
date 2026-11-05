// What happened to the link with one server: the connection log (docs/API.md).

import { useCallback, useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { formatDateTime } from '../lib/format';
import type { ConnEvent, ConnLogView, Machine } from '../lib/types';

const stamp = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

/** 850 ms, 12 s, 4 min, 2 h. */
function duration(ms = 0): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${Math.round(ms / 1000)} s`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
  return `${(ms / 3_600_000).toFixed(1)} h`;
}

const reasonText: Record<string, string> = {
  keepalive_timeout: 'the network went silent',
  connection_lost: 'the connection was closed or reset',
  closed: 'stopped on purpose',
  hostkey_unknown: 'host key not verified yet',
  hostkey_changed: 'host key changed',
  auth_needed: 'waiting for a password or passphrase',
  not_connected: 'not connected',
  disconnected: 'connection lost during the call',
  no_sftp: 'server has no SFTP',
  timeout: 'timed out',
};

const reason = (e: ConnEvent) => reasonText[e.reason ?? ''] ?? e.reason ?? e.error ?? 'unknown';

function describe(e: ConnEvent): string {
  switch (e.kind) {
    case 'hub_start':
      return `Hub started (${e.version}, pid ${e.pid})`;
    case 'hub_stop':
      return 'Hub stopped normally';
    case 'hub_unclean_exit':
      return e.lastSeen ? `Hub died unexpectedly, last alive ${formatDateTime(e.lastSeen)}` : 'Hub died unexpectedly';
    case 'connected':
      return `Connected in ${duration(e.durationMs)}${e.downtimeMs ? `, after ${duration(e.downtimeMs)} away` : ''}`;
    case 'connect_failed':
      return `Attempt ${e.attempt} failed: ${reason(e)}${e.reason && e.error ? ` (${e.error})` : ''}`;
    case 'disconnected':
      return `Disconnected, ${reason(e)}, after ${duration(e.uptimeMs)} connected${e.error ? ` (${e.error})` : ''}`;
    case 'keepalive_miss':
      return `Keepalive not answered (${e.misses} in a row)`;
    case 'link_stats':
      return `Healthy: latency ${e.rttAvgMs ?? 0} ms average, ${e.rttMaxMs ?? 0} ms max, ${e.calls ?? 0} calls, ${e.failures ?? 0} failed`;
    case 'tool_failed':
      return `${e.tool} failed after ${duration(e.durationMs)}: ${reason(e)}`;
    case 'tool_slow':
      return `${e.tool} took ${duration(e.durationMs)}`;
    case 'channel_lost':
      return 'A console lost its channel';
    default:
      return e.kind;
  }
}

function level(e: ConnEvent): string {
  switch (e.kind) {
    case 'hub_unclean_exit':
    case 'connect_failed':
    case 'tool_failed':
    case 'channel_lost':
      return 'is-bad';
    case 'disconnected':
      return e.reason === 'closed' ? '' : 'is-bad';
    case 'keepalive_miss':
    case 'tool_slow':
      return 'is-warn';
    default:
      return '';
  }
}

export function ConnectionLogDialog({ machine, onClose }: { machine: Machine; onClose: () => void }) {
  const [view, setView] = useState<ConnLogView | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setBusy(true);
    setError('');
    api
      .connectionLog(machine.id)
      .then(setView)
      .catch((err) => setError(errorMessage(err)))
      .finally(() => setBusy(false));
  }, [machine.id]);

  useEffect(load, [load]);

  const s = view?.summary;
  const items = view?.items ?? [];

  return (
    <Modal
      title={`Connection log: ${machine.name}`}
      icon="history"
      onClose={onClose}
      size="lg"
      help="Records what happened to the link. Commands, output and file contents are never written here."
      footer={
        <>
          <Button icon="refresh" onClick={load} loading={busy}>
            Refresh
          </Button>
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      {error && (
        <p className="text-danger" role="alert">
          {error}
        </p>
      )}
      {s && (
        <dl className="connlog-stats">
          <div>
            <dt>Drops</dt>
            <dd className={s.disconnects > 0 ? 'tabular text-danger' : 'tabular'}>{s.disconnects}</dd>
          </div>
          <div>
            <dt>Longest outage</dt>
            <dd className="tabular">{s.maxDowntimeMs ? duration(s.maxDowntimeMs) : '—'}</dd>
          </div>
          <div>
            <dt>Latency avg / max</dt>
            <dd className="tabular">{s.rttMaxMs ? `${s.rttAvgMs} / ${s.rttMaxMs} ms` : '—'}</dd>
          </div>
          <div>
            <dt>Failed attempts</dt>
            <dd className="tabular">{s.connectFailed}</dd>
          </div>
          <div>
            <dt>Failed tool calls</dt>
            <dd className="tabular">{s.toolFailed}</dd>
          </div>
          <div>
            <dt>Hub crashes</dt>
            <dd className={s.uncleanExits > 0 ? 'tabular text-danger' : 'tabular'}>{s.uncleanExits}</dd>
          </div>
        </dl>
      )}
      {view && items.length === 0 && <p className="text-3">Nothing recorded for this server yet.</p>}
      {items.length > 0 && (
        <div className="connlog-list">
          {items.map((e, i) => (
            <div key={`${e.at}-${i}`} className={`connlog-row ${level(e)}`}>
              <time className="connlog-time tabular" dateTime={e.at}>
                {stamp.format(new Date(e.at))}
              </time>
              <span className="connlog-text">{describe(e)}</span>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
