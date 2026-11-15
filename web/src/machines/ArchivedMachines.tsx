// Archived machines (DESIGN.md §3.15, docs/API.md Machines): not connected, not probed, absent
// from the pickers until restored. Restore brings one back as it was; Delete permanently removes
// it with its sealed credentials. Nothing on the servers changes either way.

import { useState, type MouseEvent as ReactMouseEvent } from 'react';
import { api, errorMessage, inUseBy } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { SelectBox, type SelectMode } from '../components/SelectBar';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Machine } from '../lib/types';
import { store } from '../store';
import { bulkOutcome, nameList, type BulkOutcome } from '../home/remove';

export const machineTarget = (m: Machine) =>
  m.sshConfigHost ? `ssh config · ${m.sshConfigHost}` : `${m.user ? `${m.user}@` : ''}${m.host}${m.port && m.port !== 22 ? `:${m.port}` : ''}`;

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** A refusal said as a person would: "Used by Alpha and Beta". */
function refusal(err: unknown): string {
  const users = inUseBy(err);
  if (users) return users.length ? `used by ${nameList(users.map((u) => u.name))}` : 'it is in use';
  return errorMessage(err);
}

/** Archive, restore or delete machines, one by its own call or many in one bulk request. */
export async function machinesAction(list: Machine[], action: 'archive' | 'restore' | 'delete'): Promise<BulkOutcome> {
  const ids = list.map((m) => m.id);
  const nameOf = (id: string) => list.find((m) => m.id === id)?.name ?? id;
  if (ids.length === 1) {
    const [id] = ids;
    try {
      if (action === 'archive') store.upsertMachine(await api.archiveMachine(id));
      else if (action === 'restore') store.upsertMachine(await api.restoreMachine(id));
      else {
        await api.deleteMachine(id, true);
        store.removeMachine(id);
      }
      return { done: ids, failed: [] };
    } catch (err) {
      return { done: [], failed: [{ id, name: nameOf(id), error: refusal(err) }] };
    }
  }
  const out = bulkOutcome(await api.bulkMachines(ids, action), ids, nameOf);
  const stamp = new Date().toISOString();
  for (const id of out.done) {
    const m = list.find((x) => x.id === id);
    if (action === 'delete') store.removeMachine(id);
    else if (m) store.upsertMachine({ ...m, archivedAt: action === 'archive' ? stamp : undefined });
  }
  void store.refreshMachines();
  return out;
}

/** One toast for what went through, one for what did not. */
export function reportMachines(out: BulkOutcome, list: Machine[], done: string) {
  if (out.done.length) {
    const label = out.done.length === 1 ? (list.find((m) => m.id === out.done[0])?.name ?? out.done[0]) : plural(out.done.length, 'machine');
    toast.success(`${label} ${done}.`);
  }
  if (out.failed.length) toast.error(`Not ${done}: ${out.failed.map((f) => `${f.name} (${f.error})`).join(', ')}.`);
}

export function DeleteMachinesDialog({ machines, onClose, onDone }: { machines: Machine[]; onClose: () => void; onDone?: (ids: string[]) => void }) {
  const one = machines.length === 1 ? machines[0] : null;
  return (
    <ConfirmDialog
      title={one ? `Delete ${one.name} permanently?` : `Delete ${machines.length} machines permanently?`}
      confirmLabel="Delete permanently"
      danger
      onClose={onClose}
      onConfirm={async () => {
        const out = await machinesAction(machines, 'delete');
        if (out.done.length) onDone?.(out.done);
        reportMachines({ ...out, failed: [] }, machines, 'deleted');
        if (out.failed.length) throw new Error(`Not deleted: ${out.failed.map((f) => `${f.name} (${f.error})`).join(', ')}.`);
      }}
    >
      <p>
        {one ? 'Its' : 'Their'} connection settings, sealed credentials and the host keys only {one ? 'it' : 'they'} used are removed from the hub. Nothing on {one ? 'the server' : 'the servers'} changes.
      </p>
      {!one && <p className="text-3 fs-sm">{nameList(machines.map((m) => m.name), 4)}</p>}
    </ConfirmDialog>
  );
}

function ArchivedMachineRow({ m, select, onDelete }: { m: Machine; select: SelectMode; onDelete: (list: Machine[]) => void }) {
  const [busy, setBusy] = useState(false);
  const selected = select.has(m.id);
  const restore = async () => {
    setBusy(true);
    const out = await machinesAction([m], 'restore');
    reportMachines(out, [m], 'restored');
    if (out.failed.length) setBusy(false);
  };
  const onPick = (e: ReactMouseEvent) => {
    if ((e.target as HTMLElement).closest('.archived-actions')) return;
    if (select.on || e.shiftKey || e.ctrlKey || e.metaKey) {
      e.preventDefault();
      select.click(m.id, e);
    }
  };
  return (
    <article
      className={`archived-row ${select.on ? 'is-selecting' : ''} ${selected ? 'is-selected' : ''}`}
      aria-label={m.name}
      onClick={onPick}
      onMouseDown={(e) => {
        if (e.shiftKey) e.preventDefault();
      }}
      {...select.pressProps(m.id)}
    >
      {select.on && (
        <button type="button" className="archived-pick" role="checkbox" aria-checked={selected} aria-label={`Select ${m.name}`}>
          <SelectBox checked={selected} />
        </button>
      )}
      <span className="card-icon" aria-hidden="true">
        <Icon name="server" />
      </span>
      <div className="archived-main">
        <h3 className="truncate">{m.name}</h3>
        <span className="archived-when mono" title={machineTarget(m)}>
          {machineTarget(m)}
        </span>
        <span className="archived-when tabular" title={m.archivedAt ? `Archived ${formatDateTime(m.archivedAt)}` : undefined}>
          Archived {m.archivedAt ? timeAgo(m.archivedAt) : ''}
        </span>
      </div>
      <div className="archived-actions">
        <Button size="sm" icon="restart" loading={busy} disabled={busy || select.on} onClick={() => void restore()}>
          Restore
        </Button>
        <Button size="sm" variant="danger-ghost" icon="trash" disabled={busy || select.on} onClick={() => onDelete([m])} aria-label={`Delete ${m.name} permanently`} title="Delete permanently">
          Delete permanently
        </Button>
      </div>
    </article>
  );
}

export function ArchivedMachines({ machines, select, onDelete }: { machines: Machine[]; select: SelectMode; onDelete: (list: Machine[]) => void }) {
  if (!machines.length) {
    return (
      <div className="card home-empty">
        <div className="empty-state">
          <h2>Nothing archived</h2>
          <p>An archived machine keeps its settings and comes back with Restore.</p>
        </div>
      </div>
    );
  }
  return (
    <div className="card archived-list" role="list" aria-label="Archived machines">
      {machines.map((m) => (
        <div role="listitem" key={m.id}>
          <ArchivedMachineRow m={m} select={select} onDelete={onDelete} />
        </div>
      ))}
    </div>
  );
}
