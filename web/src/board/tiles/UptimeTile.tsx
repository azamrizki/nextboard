// Uptime card (docs/API-uptime.md): a project's monitors on the left, the chosen one's page on
// the right. The hub does the checking, so this only shows it, and every Uptime card of a
// project shows the same monitors. A narrow card shows one side at a time.

import { memo, useEffect, useMemo, useState } from 'react';
import { useReactFlow, type NodeProps } from '@xyflow/react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import type { MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { copyText } from '../../lib/clipboard';
import { formatDateTime } from '../../lib/format';
import { projectParts } from '../../lib/parts';
import { useMachine } from '../../store';
import { AlertsDialog } from '../../uptime/AlertsDialog';
import { MaintenanceDialog } from '../../uptime/MaintenanceDialog';
import { PublicPageDialog } from '../../uptime/PublicPageDialog';
import { uptimeApi, type Monitor, type UptimeEvent } from '../../uptime/api';
import { byUrgency, dateTime, duration, millis, percent, STATUS_BADGE, STATUS_DOT, STATUS_LABEL } from '../../uptime/format';
import { Heartbeats, HeartbeatsDetail } from '../../uptime/Heartbeats';
import { MonitorForm } from '../../uptime/MonitorForm';
import { ResponseChart } from '../../uptime/ResponseChart';
import { clearWantedMonitor, putMonitor, useUptime, useWantedMonitor } from '../../uptime/store';
import type { FlowNode } from '../flow';
import { readUptime } from '../kinds';
import { useBoardEnv } from '../ui';
import { InlineTitle, TileFrame, TileHead } from './common';

/** More monitors than this, and the list gets a search box. */
const SEARCH_FROM = 8;
const CERT_WARN_DAYS = 14;

function Stat({ label, value, sub, warn }: { label: string; value: string; sub?: string; warn?: boolean }) {
  return (
    <div className="stat-tile">
      <span className="stat-label">{label}</span>
      <span className="stat-value tabular">
        {warn && <Icon name="bell" className="up-stat-warn" />}
        {value}
      </span>
      {sub && <span className="stat-sub truncate">{sub}</span>}
    </div>
  );
}

function StatusBadge({ m }: { m: Monitor }) {
  const icon = m.status === 'up' ? 'check' : m.status === 'down' ? 'alertCircle' : m.status === 'paused' ? 'pause' : m.status === 'maintenance' ? 'calendar' : 'clock';
  return (
    <span className={`badge ${STATUS_BADGE[m.status]} up-status`}>
      <Icon name={icon} />
      {STATUS_LABEL[m.status]}
    </span>
  );
}

function PushPanel({ m }: { m: Monitor }) {
  const url = `${location.origin}/api/push/${m.pushToken}?status=up&msg=OK&ping=`;
  return (
    <section className="up-push">
      <h4 className="up-section-title">Push URL</h4>
      <p className="up-form-note">
        Call it at least every {m.intervalSec} s. Add <code>status=down</code> to report a failure yourself. Whoever has this URL can report for this monitor, and nothing else.
      </p>
      <div className="up-push-row">
        <code className="up-push-url truncate">{url}</code>
        <Button
          size="sm"
          icon="copy"
          onClick={() => {
            void copyText(url).then((ok) => (ok ? toast.success('Push URL copied.') : toast.error('Could not copy. Select the URL by hand.')));
          }}
        >
          Copy
        </Button>
      </div>
    </section>
  );
}

function Events({ m }: { m: Monitor }) {
  const [events, setEvents] = useState<UptimeEvent[] | null>(null);
  // A new event comes with a new "since".
  useEffect(() => {
    let stop = false;
    uptimeApi
      .events(m.id)
      .then((r) => !stop && setEvents(r.items ?? []))
      .catch(() => !stop && setEvents([]));
    return () => {
      stop = true;
    };
  }, [m.id, m.since]);
  return (
    <section>
      <h4 className="up-section-title">Important events</h4>
      {!events ? null : !events.length ? (
        <p className="up-form-note">It has not gone down or come back yet.</p>
      ) : (
        <div className="tokens-table-wrap nowheel up-table-wrap">
          <table className="tokens-table up-events">
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col">Status</th>
                <th scope="col">Message</th>
                <th scope="col" className="num">
                  After
                </th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.t}>
                  <th scope="row" className="tabular">
                    {dateTime(e.t)}
                  </th>
                  <td>
                    <span className={`badge ${e.status === 'up' ? 'badge-success' : 'badge-danger'}`}>{e.status === 'up' ? 'Up' : 'Down'}</span>
                  </td>
                  <td className="up-events-msg" title={e.msg}>
                    {e.msg}
                  </td>
                  <td className="num tabular">{e.durSec ? duration(e.durSec) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Detail({ m, onBack, onEdit, onDelete }: { m: Monitor; onBack: () => void; onEdit: () => void; onDelete: () => void }) {
  const machine = useMachine(m.machineId || 'local');
  const [busy, setBusy] = useState(false);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const days = m.cert ? Math.floor((new Date(m.cert.notAfter).getTime() - Date.now()) / 86_400_000) : 0;
  const since = m.since ? (Date.now() - new Date(m.since).getTime()) / 1000 : 0;
  return (
    <div className="up-detail nowheel">
      <header className="up-detail-head">
        <Button size="sm" variant="ghost" iconOnly icon="arrowLeft" className="up-back" aria-label="Back to the list" onClick={onBack} />
        <div className="up-detail-title">
          <h3 className="truncate">{m.name}</h3>
          {m.type === 'http' && m.url ? (
            <a className="up-target truncate" href={m.url} target="_blank" rel="noreferrer noopener">
              {m.target}
            </a>
          ) : (
            <span className="up-target truncate">{m.target}</span>
          )}
          {!!m.tags?.length && (
            <span className="up-detail-tags">
              {m.tags.map((t) => (
                <span key={t} className="badge">
                  {t}
                </span>
              ))}
            </span>
          )}
        </div>
        <span className="spacer" />
        {m.type !== 'push' && (
          <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Check now" title="Check now" disabled={!m.active || busy} onClick={() => void act(() => uptimeApi.check(m.id))} />
        )}
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={m.active ? 'pause' : 'play'}
          aria-label={m.active ? 'Pause' : 'Resume'}
          title={m.active ? 'Pause' : 'Resume'}
          disabled={busy}
          onClick={() => void act(async () => putMonitor(await uptimeApi.update(m.id, { active: !m.active })))}
        />
        <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label="Edit" title="Edit" onClick={onEdit} />
        <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Delete" title="Delete" onClick={onDelete} />
      </header>

      <div className="up-now">
        <StatusBadge m={m} />
        <span className="up-now-text">
          {m.since && (m.status === 'up' || m.status === 'down') ? `for ${duration(since)}${m.msg ? ' · ' : ''}` : ''}
          {m.msg ? <span className="up-now-msg">{m.msg}</span> : null}
        </span>
      </div>

      <HeartbeatsDetail
        beats={m.recent ?? []}
        count={60}
        idle={m.type === 'push' ? `Expected every ${m.intervalSec} s` : `Checks every ${m.intervalSec} s${machine && m.machineId ? ` from ${machine.name}` : ''}`}
      />

      <div className="stat-tiles up-stats">
        <Stat label="Response" value={millis(m.ms)} sub="last check" />
        <Stat label="Average" value={m.avgMs24h ? millis(m.avgMs24h) : '—'} sub="24 hours" />
        <Stat label="Uptime" value={percent(m.uptime24h)} sub="24 hours" />
        <Stat label="Uptime" value={percent(m.uptime30d)} sub="30 days" />
        {m.cert && <Stat label="Certificate" value={days < 0 ? 'Expired' : `${days} days`} sub={m.cert.issuer || formatDateTime(m.cert.notAfter)} warn={days <= CERT_WARN_DAYS} />}
      </div>

      {m.type === 'push' && <PushPanel m={m} />}
      <ResponseChart monitorId={m.id} lastCheck={m.lastCheck} />
      <Events m={m} />
    </div>
  );
}

function Row({ m, active, onPick }: { m: Monitor; active: boolean; onPick: () => void }) {
  const { project } = useBoardEnv();
  // the part of a project in several parts it watches (ROADMAP #49)
  const part = m.partId ? projectParts(project).find((p) => p.id === m.partId) : undefined;
  return (
    <li>
      <button type="button" className={`up-row ${active ? 'is-selected' : ''}`} aria-current={active || undefined} onClick={onPick}>
        <span className="up-row-top">
          <span className={`dot dot-${STATUS_DOT[m.status]}`} aria-hidden="true" />
          <span className="up-row-name truncate">{m.name}</span>
          {part && <span className="cap-chip up-row-part">{part.name}</span>}
          <span className="sr-only"> {STATUS_LABEL[m.status]}</span>
          <span className="up-row-pct tabular" title="Uptime, 24 hours">
            {m.status === 'paused' ? 'Paused' : percent(m.uptime24h)}
          </span>
        </span>
        <Heartbeats beats={m.recent ?? []} count={24} />
      </button>
    </li>
  );
}

function UptimeTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project } = useBoardEnv();
  const rf = useReactFlow();
  const d = readUptime(data.node);
  const { ready, error, monitors, settings } = useUptime(project.id);
  const wanted = useWantedMonitor();
  const [picked, setPicked] = useState('');
  const [query, setQuery] = useState('');
  const [form, setForm] = useState<'add' | Monitor | null>(null);
  const [alerts, setAlerts] = useState(false);
  const [publicPage, setPublicPage] = useState(false);
  const [maintenance, setMaintenance] = useState(false);
  const [tag, setTag] = useState('');
  const [remove, setRemove] = useState<Monitor | null>(null);
  const [delCard, setDelCard] = useState(false);

  const sorted = useMemo(() => [...monitors].sort(byUrgency), [monitors]);
  const allTags = useMemo(() => [...new Set(monitors.flatMap((m) => m.tags ?? []))].sort((a, b) => a.localeCompare(b)), [monitors]);
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return sorted.filter(
      (m) =>
        (!tag || m.tags?.includes(tag)) &&
        (!q || m.name.toLowerCase().includes(q) || m.target.toLowerCase().includes(q) || (m.group ?? '').toLowerCase().includes(q) || (m.tags ?? []).some((t) => t.toLowerCase().includes(q))),
    );
  }, [sorted, query, tag]);
  // By group, groups by name, the ungrouped last; within one, the most urgent first.
  const groups = useMemo(() => {
    if (!shown.some((m) => m.group)) return [{ name: '', items: shown }];
    const by = new Map<string, Monitor[]>();
    for (const m of shown) by.set(m.group ?? '', [...(by.get(m.group ?? '') ?? []), m]);
    return [...by.entries()].sort(([a], [b]) => (!a ? 1 : !b ? -1 : a.localeCompare(b))).map(([name, items]) => ({ name, items }));
  }, [shown]);
  // Nothing picked: the most urgent one, on a card wide enough to show it next to the list.
  const current = monitors.find((m) => m.id === picked) ?? sorted[0];

  // Clicked in a notification: show it, and bring the card into view.
  useEffect(() => {
    if (!wanted || !monitors.some((m) => m.id === wanted.monitorId)) return;
    setPicked(wanted.monitorId);
    void rf.fitView({ nodes: [{ id }], duration: 300, maxZoom: 1 });
    clearWantedMonitor();
  }, [wanted, monitors, rf, id]);

  const down = monitors.filter((m) => m.status === 'down').length;
  const meta = !monitors.length ? '' : down ? `${down} down of ${monitors.length}` : monitors.length === 1 ? '1 monitor' : `${monitors.length} monitors`;
  const maintOn = monitors.filter((m) => m.status === 'maintenance').length;
  const menu: MenuEntry[] = [
    { label: 'Alerts…', icon: 'bell', onSelect: () => setAlerts(true) },
    { label: 'Maintenance…', icon: 'calendar', meta: maintOn ? `${maintOn} on` : undefined, onSelect: () => setMaintenance(true) },
    { label: 'Public status page…', icon: 'globe', onSelect: () => setPublicPage(true) },
  ];

  return (
    <TileFrame id={id} kind="uptime" selected={!!selected} label={`Uptime ${d.title}`} minW={420} minH={320}>
      <TileHead
        id={id}
        icon="activity"
        title={<InlineTitle value={d.title} label="Card title" maxLength={40} onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={meta && <span className={`tile-meta tabular ${down ? 'up-meta-down' : ''}`}>{meta}</span>}
        actions={
          <Button size="sm" variant="ghost" icon="plus" className="nodrag" onClick={() => setForm('add')}>
            Add monitor
          </Button>
        }
        menu={menu}
        menuLabel={`Actions for ${d.title}`}
        deleteItem={{ label: 'Delete card…', icon: 'trash', danger: true, onSelect: () => setDelCard(true) }}
      />
      <div className={`up-body nodrag ${picked ? 'is-picked' : ''}`}>
        {error ? (
          <div className="alert up-load-error" role="alert">
            {error}
          </div>
        ) : !ready ? null : !monitors.length ? (
          <div className="up-empty">
            <Icon name="activity" />
            <h3>No monitors yet</h3>
            <p>Add a site, a port or a server. The hub keeps checking it and tells you when it goes down, whether a board is open or not.</p>
            <Button variant="primary" size="sm" icon="plus" onClick={() => setForm('add')}>
              Add monitor
            </Button>
          </div>
        ) : (
          <div className="up-grid">
            <div className="up-side">
              {monitors.length >= SEARCH_FROM && (
                <input className="control control-sm up-search" type="search" placeholder="Search monitors" aria-label="Search monitors" value={query} onChange={(e) => setQuery(e.target.value)} />
              )}
              {allTags.length > 0 && (
                <div className="chip-row up-tags" role="group" aria-label="Show monitors with a tag">
                  {allTags.map((t) => (
                    <button key={t} type="button" className={`chip-btn ${tag === t ? 'on' : ''}`} aria-pressed={tag === t} onClick={() => setTag(tag === t ? '' : t)}>
                      {t}
                    </button>
                  ))}
                </div>
              )}
              <ul className="plain up-list nowheel" aria-label="Monitors">
                {groups.map((g) => [
                  g.name || groups.length > 1 ? (
                    <li key={`g:${g.name}`} className="up-group-head" role="presentation">
                      {g.name || 'Other'}
                    </li>
                  ) : null,
                  ...g.items.map((m) => <Row key={m.id} m={m} active={m.id === current?.id} onPick={() => setPicked(m.id)} />),
                ])}
                {!shown.length && <li className="up-form-note up-no-match">No monitor matches.</li>}
              </ul>
            </div>
            {current && <Detail key={current.id} m={current} onBack={() => setPicked('')} onEdit={() => setForm(current)} onDelete={() => setRemove(current)} />}
          </div>
        )}
      </div>
      {form && (
        <MonitorForm
          projectId={project.id}
          monitor={form === 'add' ? undefined : form}
          onClose={() => setForm(null)}
          onSaved={(m) => setPicked(m.id)}
        />
      )}
      {alerts && <AlertsDialog projectId={project.id} settings={settings} onClose={() => setAlerts(false)} />}
      {maintenance && <MaintenanceDialog projectId={project.id} settings={settings} monitors={monitors} onClose={() => setMaintenance(false)} />}
      {publicPage && <PublicPageDialog projectId={project.id} settings={settings} publicCount={monitors.filter((m) => m.public).length} onClose={() => setPublicPage(false)} />}
      {remove && (
        <ConfirmDialog
          title={`Delete ${remove.name}?`}
          confirmLabel="Delete monitor"
          danger
          onClose={() => setRemove(null)}
          onConfirm={async () => {
            await uptimeApi.remove(remove.id);
            if (picked === remove.id) setPicked('');
          }}
        >
          <p>Checking stops and its history is gone for good. To only stop it for a while, pause it.</p>
        </ConfirmDialog>
      )}
      {delCard && (
        <ConfirmDialog title={`Delete ${d.title}?`} confirmLabel="Delete card" danger onClose={() => setDelCard(false)} onConfirm={() => ctl.deleteNodes([id])}>
          <p>Only the card goes. {monitors.length ? 'The hub keeps checking these monitors and alerting you. Add the card again to see them, or delete a monitor to stop it.' : ''}</p>
        </ConfirmDialog>
      )}
    </TileFrame>
  );
}

export const UptimeTile = memo(UptimeTileImpl);
