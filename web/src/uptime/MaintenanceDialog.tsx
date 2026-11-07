// Maintenance windows of a project (docs/API-uptime.md): while one is on, its monitors are not
// checked, not counted, and nobody is alerted. The list is saved as a whole.

import { useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, Field, SegmentedControl, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { uptimeApi, type MaintenanceWindow, type Monitor, type UptimeSettings } from './api';
import { setUptimeSettings } from './store';

type Mode = MaintenanceWindow['mode'];
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const browserTZ = () => Intl.DateTimeFormat().resolvedOptions().timeZone || '';

/** An ISO time as a datetime-local value, in this browser's zone. */
function toLocalInput(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(v: string): string | undefined {
  if (!v) return undefined;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** One line on what a window does. */
export function windowSummary(w: MaintenanceWindow, monitors: Monitor[]): string {
  const which = !w.monitorIds?.length ? 'all monitors' : w.monitorIds.length === 1 ? (monitors.find((m) => m.id === w.monitorIds![0])?.name ?? '1 monitor') : `${w.monitorIds.length} monitors`;
  if (w.mode === 'manual') return `${w.active ? 'On now' : 'Off'} · ${which}`;
  if (w.mode === 'once') {
    const f = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : '?');
    return `${f(w.start)} to ${f(w.end)} · ${which}`;
  }
  const days = (w.days ?? []).map((d) => DAYS[d]).join(', ');
  const h = Math.floor((w.minutes ?? 0) / 60);
  const m = (w.minutes ?? 0) % 60;
  return `${days} at ${w.from} for ${h ? `${h} h` : ''}${h && m ? ' ' : ''}${m ? `${m} min` : ''}${w.tz ? ` (${w.tz})` : ''} · ${which}`;
}

function WindowForm({ initial, monitors, onDone, onCancel }: { initial: MaintenanceWindow; monitors: Monitor[]; onDone: (w: MaintenanceWindow) => void; onCancel: () => void }) {
  const [w, setW] = useState<MaintenanceWindow>(initial);
  const [start, setStart] = useState(toLocalInput(initial.start));
  const [end, setEnd] = useState(toLocalInput(initial.end));
  const all = !w.monitorIds?.length;
  const set = (patch: Partial<MaintenanceWindow>) => setW((x) => ({ ...x, ...patch }));
  const toggleMonitor = (id: string, on: boolean) => {
    const cur = w.monitorIds ?? [];
    set({ monitorIds: on ? [...cur, id] : cur.filter((x) => x !== id) });
  };
  const toggleDay = (d: number) => {
    const cur = w.days ?? [];
    set({ days: cur.includes(d) ? cur.filter((x) => x !== d) : [...cur, d].sort() });
  };
  return (
    <div className="up-form up-maint-form" role="group" aria-label="Maintenance window">
      <TextField label="Title" required maxLength={80} placeholder="Weekly backup" value={w.title} onChange={(e) => set({ title: e.target.value })} autoFocus />
      <Field label="When" as="group">
        {(a) => (
          <SegmentedControl<Mode>
            block
            labelledBy={a.labelId}
            value={w.mode}
            onChange={(mode) => set({ mode, ...(mode === 'weekly' && !w.from ? { from: '02:00', minutes: 60, days: [0], tz: browserTZ() } : {}) })}
            options={[
              { value: 'manual', label: 'Now, by hand' },
              { value: 'once', label: 'Once' },
              { value: 'weekly', label: 'Every week' },
            ]}
          />
        )}
      </Field>
      {w.mode === 'manual' && <Checkbox checked={!!w.active} onChange={(active) => set({ active })} label="On now: checks stop until it is turned off" />}
      {w.mode === 'once' && (
        <div className="up-form-row">
          <TextField label="From" type="datetime-local" value={start} onChange={(e) => setStart(e.target.value)} fieldClassName="up-grow" />
          <TextField label="To" type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} fieldClassName="up-grow" />
        </div>
      )}
      {w.mode === 'weekly' && (
        <>
          <Field label="Days" as="group">
            {(a) => (
              <div className="chip-row up-days" role="group" aria-labelledby={a.labelId}>
                {DAYS.map((d, i) => (
                  <button key={d} type="button" className={`chip-btn ${w.days?.includes(i) ? 'on' : ''}`} aria-pressed={!!w.days?.includes(i)} onClick={() => toggleDay(i)}>
                    {d}
                  </button>
                ))}
              </div>
            )}
          </Field>
          <div className="up-form-row">
            <TextField label="Starts at" type="time" value={w.from ?? ''} onChange={(e) => set({ from: e.target.value })} fieldClassName="up-grow" />
            <TextField label="Lasts (min)" inputMode="numeric" value={String(w.minutes ?? '')} onChange={(e) => set({ minutes: Number(e.target.value) || 0 })} fieldClassName="up-grow" />
            <TextField label="Time zone" mono value={w.tz ?? ''} placeholder={browserTZ()} onChange={(e) => set({ tz: e.target.value.trim() })} fieldClassName="up-grow" />
          </div>
        </>
      )}
      {monitors.length > 1 && (
        <Field label="Monitors" as="group">
          {() => (
            <div className="up-maint-monitors">
              <Checkbox checked={all} onChange={(on) => set({ monitorIds: on ? [] : [monitors[0].id] })} label="All monitors of this project" />
              {!all &&
                monitors.map((m) => (
                  <Checkbox key={m.id} checked={!!w.monitorIds?.includes(m.id)} onChange={(on) => toggleMonitor(m.id, on)} label={m.name} />
                ))}
            </div>
          )}
        </Field>
      )}
      <div className="up-maint-form-actions">
        <Button size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" disabled={!w.title.trim()} onClick={() => onDone({ ...w, title: w.title.trim(), start: w.mode === 'once' ? fromLocalInput(start) : undefined, end: w.mode === 'once' ? fromLocalInput(end) : undefined })}>
          Save window
        </Button>
      </div>
    </div>
  );
}

export function MaintenanceDialog({ projectId, settings, monitors, onClose }: { projectId: string; settings: UptimeSettings; monitors: Monitor[]; onClose: () => void }) {
  const [list, setList] = useState<MaintenanceWindow[]>(settings.maintenance ?? []);
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const save = async (next: MaintenanceWindow[], close: boolean) => {
    setBusy(true);
    setError('');
    try {
      const s = await uptimeApi.setMaintenance(projectId, next);
      setUptimeSettings(projectId, s);
      setList(s.maintenance ?? []);
      setEditing(null);
      if (close) onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Maintenance"
      icon="calendar"
      onClose={onClose}
      footer={
        <Button onClick={onClose}>Close</Button>
      }
    >
      <div className="up-form">
        <p className="up-form-note">While a window is on, its monitors are not checked, their uptime does not count it, and nobody is alerted. The public page says Maintenance, without the title.</p>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        {list.length > 0 && (
          <ul className="plain up-maint-list" aria-label="Maintenance windows">
            {list.map((w, i) =>
              editing === i ? (
                <li key={w.id ?? i}>
                  <WindowForm initial={w} monitors={monitors} onCancel={() => setEditing(null)} onDone={(nw) => void save(list.map((x, j) => (j === i ? nw : x)), false)} />
                </li>
              ) : (
                <li key={w.id ?? i} className="up-maint-row">
                  <Icon name="calendar" />
                  <span className="up-maint-text">
                    <span className="up-maint-title truncate">{w.title}</span>
                    <span className="up-form-note truncate">{windowSummary(w, monitors)}</span>
                  </span>
                  {w.mode === 'manual' && (
                    <Button size="sm" disabled={busy} onClick={() => void save(list.map((x, j) => (j === i ? { ...x, active: !x.active } : x)), false)}>
                      {w.active ? 'Turn off' : 'Turn on'}
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label={`Edit ${w.title}`} disabled={busy || editing !== null} onClick={() => setEditing(i)} />
                  <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label={`Delete ${w.title}`} disabled={busy} onClick={() => void save(list.filter((_, j) => j !== i), false)} />
                </li>
              ),
            )}
          </ul>
        )}
        {editing === 'new' ? (
          <WindowForm initial={{ title: '', mode: 'manual', active: true }} monitors={monitors} onCancel={() => setEditing(null)} onDone={(w) => void save([...list, w], false)} />
        ) : (
          <Button size="sm" icon="plus" disabled={busy || editing !== null} onClick={() => setEditing('new')}>
            Add window
          </Button>
        )}
      </div>
    </Modal>
  );
}
