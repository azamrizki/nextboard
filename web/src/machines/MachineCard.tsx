// One machine as a bento card (DESIGN.md §3.3): status, probe info, persistence, actions. In
// select mode (§3.18) it has a box to pick it, unless it is in use: then it says by what.

import { useRef, useState, type MouseEvent as ReactMouseEvent, type SyntheticEvent } from 'react';
import { useAgentCatalog } from '../agents/catalog';
import { useEffectiveSettings } from '../agents/settings';
import { api, errorMessage } from '../api';
import { machineStatus } from '../board/sessionStatus';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { SelectBox, type SelectMode } from '../components/SelectBar';
import { StatusBadge } from '../components/StatusDot';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Machine, PersistMode } from '../lib/types';
import { store } from '../store';
import { DeleteMachinesDialog, machinesAction, machineTarget, reportMachines } from './ArchivedMachines';
import { ConnectionLogDialog } from './ConnectionLogDialog';
import { HostKeyMismatchWarning } from './HostKeyDialogs';
import { OrphansDialog } from './OrphansDialog';

interface MachineCardProps {
  machine: Machine;
  /** Open the TOFU dialog for this machine. */
  onVerifyHostKey: (m: Machine) => void;
  /** Open the password / passphrase dialog for this machine. */
  onEnterAuth: (m: Machine) => void;
  /** Select mode of the list (§3.18). */
  select?: SelectMode;
  /** Why it cannot be archived or deleted ("Used by Alpha"), '' when it can. */
  blocked?: string;
}

export function MachineCard({ machine: m, onVerifyHostKey, onEnterAuth, select, blocked = '' }: MachineCardProps) {
  const [busy, setBusy] = useState<string>('');
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(m.name);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [orphans, setOrphans] = useState(false);
  const [connLog, setConnLog] = useState(false);
  const renamingRef = useRef(false); // Enter + blur must not both commit
  const settings = useEffectiveSettings();
  const catalog = useAgentCatalog();

  const isSsh = m.kind === 'ssh';
  const status = machineStatus(m);
  const probe = m.probe;

  /** Run an action returning the updated Machine. */
  const act = async (label: string, fn: () => Promise<Machine>) => {
    setBusy(label);
    try {
      const updated = await fn();
      store.upsertMachine(updated);
      if (updated.status === 'hostkey' && updated.pendingHostKey) onVerifyHostKey(updated);
      if (updated.needsAuth) onEnterAuth(updated);
      return updated;
    } catch (err) {
      toast.error(`${label} failed for ${m.name}: ${errorMessage(err)}`);
      return null;
    } finally {
      setBusy('');
    }
  };

  const startRename = () => {
    setDraft(m.name);
    renamingRef.current = true;
    setRenaming(true);
  };

  const cancelRename = () => {
    renamingRef.current = false;
    setRenaming(false);
  };

  const rename = async (e: SyntheticEvent) => {
    e.preventDefault();
    if (!renamingRef.current) return;
    renamingRef.current = false;
    const name = draft.trim();
    setRenaming(false);
    if (!name || name === m.name) return;
    await act('Rename', () => api.updateMachine(m.id, { name }));
  };

  const setPersist = (mode: PersistMode) => act('Change persistence', () => api.updateMachine(m.id, { persistMode: mode }));

  const target = machineTarget(m);
  const selecting = !!select?.on;
  const selected = !!select?.has(m.id);

  const archive = async () => {
    setBusy('Archive');
    const out = await machinesAction([m], 'archive');
    reportMachines(out, [m], 'archived');
    setBusy('');
  };

  // a click on the card picks it in select mode, except on its own controls
  const onPick = (e: ReactMouseEvent) => {
    if (!select || blocked || (e.target as HTMLElement).closest('button:not(.machine-pick), input, a, select, .menu, .segmented')) return;
    if (selecting || ((e.shiftKey || e.ctrlKey || e.metaKey) && (e.target as HTMLElement).closest('.machine-pick'))) {
      e.preventDefault();
      select.click(m.id, e);
    }
  };

  // curl is looked up for the hooks' fallback (API-agents §4.5); it is no agent
  const off = new Set(catalog.agents.filter((a) => settings.agents.disabled?.[a.id]).map((a) => a.detect));
  const agents = Object.entries(probe?.commands ?? {})
    .filter(([cmd]) => cmd !== 'curl' && !off.has(cmd))
    .sort(([a], [b]) => a.localeCompare(b));
  const canConnect = isSsh && !['connected', 'connecting'].includes(m.status);
  const more: MenuEntry[] = [
    { label: 'Rename', icon: 'pencil', disabled: !!busy, onSelect: startRename },
    ...(isSsh
      ? ([
          { label: 'Orphans', icon: 'broom', disabled: !!busy, onSelect: () => setOrphans(true) },
          { label: 'Connection log', icon: 'history', onSelect: () => setConnLog(true) },
          ...(select ? [{ label: 'Select', icon: 'selectAll', disabled: !!blocked, title: blocked || undefined, onSelect: () => select.start(m.id) }] : []),
          'sep',
          { label: 'Archive', icon: 'archive', disabled: !!busy || !!blocked, title: blocked || 'Not connected and out of the pickers until restored', onSelect: () => void archive() },
          { label: 'Delete permanently…', icon: 'trash', danger: true, disabled: !!busy || !!blocked, title: blocked || undefined, onSelect: () => setConfirmDelete(true) },
        ] as MenuEntry[])
      : []),
  ];

  return (
    <article
      className={`card machine-card tone-${status.color} ${m.hostKeyMismatch ? 'card-danger' : ''} ${selecting ? 'is-selecting' : ''} ${selected ? 'is-selected' : ''} ${selecting && blocked ? 'is-blocked' : ''}`}
      aria-label={m.name}
      onClick={onPick}
      onMouseDown={(e) => {
        if (e.shiftKey && selecting) e.preventDefault();
      }}
      {...(blocked ? {} : select?.pressProps(m.id))}
    >
      {selecting && (
        <div className="machine-select">
          <button type="button" className="machine-pick" role="checkbox" aria-checked={selected} aria-label={`Select ${m.name}`} disabled={!!blocked} title={blocked || undefined}>
            <SelectBox checked={selected} disabled={!!blocked} />
            <span>{blocked ? 'Cannot be archived' : selected ? 'Selected' : 'Select'}</span>
          </button>
          {blocked && <span className="machine-use">{blocked}</span>}
        </div>
      )}
      <header className="card-head">
        <span className="card-icon" aria-hidden="true">
          <Icon name={isSsh ? 'server' : 'monitor'} />
        </span>
        <div className="card-title">
          {renaming ? (
            <form onSubmit={rename}>
              <input
                className="control control-sm"
                value={draft}
                autoFocus
                onChange={(e) => setDraft(e.target.value)}
                onBlur={rename}
                onKeyDown={(e) => e.key === 'Escape' && cancelRename()}
                aria-label="Machine name"
                maxLength={64}
              />
            </form>
          ) : (
            <h3 className="truncate" title={m.name}>
              {m.name}
            </h3>
          )}
          <span className="card-meta truncate" title={isSsh ? target : undefined}>
            {isSsh ? target : 'The computer running the hub'}
          </span>
        </div>
        <StatusBadge color={status.color} label={status.label} title={status.detail} />
      </header>

      {m.hostKeyMismatch && <HostKeyMismatchWarning machine={m} />}

      {!m.hostKeyMismatch && m.status === 'hostkey' && m.pendingHostKey && (
        <div className="warning-panel">
          <Icon name="shield" />
          <span className="grow">Verify the {m.pendingHostKey.keyType} host key before connecting.</span>
          <Button size="sm" variant="primary" onClick={() => onVerifyHostKey(m)}>
            Verify host key
          </Button>
        </div>
      )}

      {m.needsAuth && (
        <div className="warning-panel">
          <Icon name="key" />
          <span className="grow">{m.needsAuth.kind === 'passphrase' ? 'Its SSH key is locked with a passphrase.' : `${m.needsAuth.host} asks for a password.`}</span>
          <Button size="sm" variant="primary" onClick={() => onEnterAuth(m)}>
            {m.needsAuth.kind === 'passphrase' ? 'Enter passphrase' : 'Enter password'}
          </Button>
        </div>
      )}

      {m.error && m.status === 'error' && !m.hostKeyMismatch && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{m.error}</span>
        </div>
      )}

      <div className="machine-body">
        <dl className="machine-stats">
          <div className="machine-stat">
            <dt>Terminals</dt>
            <dd className="tabular">{m.sessionCount}</dd>
          </div>
          <div className="machine-stat">
            <dt>Agents</dt>
            <dd className="tabular">{probe ? agents.filter(([, path]) => path).length : '—'}</dd>
          </div>
          <div className="machine-stat">
            <dt>Probed</dt>
            <dd className="tabular truncate" title={probe?.probedAt ? formatDateTime(probe.probedAt) : undefined}>
              {probe?.probedAt ? timeAgo(probe.probedAt) : '—'}
            </dd>
          </div>
        </dl>

        {probe ? (
          <>
            <dl className="machine-spec">
              <div>
                <dt>
                  <Icon name="cpu" />
                  System
                </dt>
                <dd>
                  {probe.os} {probe.arch}
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="terminal" />
                  Shell
                </dt>
                <dd className="mono truncate" title={probe.shell}>
                  {probe.shell || '—'}
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="home" />
                  Home
                </dt>
                <dd className="mono truncate" title={probe.home}>
                  {probe.home || '—'}
                </dd>
              </div>
              <div>
                <dt>
                  <Icon name="layers" />
                  tmux
                </dt>
                <dd>
                  <span className={`badge ${probe.tmux ? 'badge-success' : ''}`}>{probe.tmux ? 'Available' : 'Not found'}</span>
                </dd>
              </div>
            </dl>
            <div className="machine-agents">
              <span className="machine-label">Agent CLIs</span>
              {agents.length === 0 ? (
                <span className="fs-sm text-3">{isSsh ? 'None here. Agents run on This PC.' : 'None found.'}</span>
              ) : (
                <span className="chips">
                  {agents.map(([cmd, path]) => (
                    <span key={cmd} className={`badge badge-mono ${path ? 'badge-success' : ''}`} title={path || `${cmd}: not found`}>
                      <Icon name={path ? 'check' : 'close'} />
                      {cmd}
                    </span>
                  ))}
                </span>
              )}
            </div>
          </>
        ) : (
          <div className="machine-empty">
            <Icon name="server" />
            <span>Not probed yet</span>
            <span className="fs-xs text-3">Its system, shell and tools show here once it connects.</span>
          </div>
        )}

        {isSsh && (
          <div className="machine-persist">
            <div className="machine-persist-label">
              <span className="machine-label" id={`persist-${m.id}`}>
                Persistence
              </span>
              {m.effectivePersist && <span className="fs-xs text-3">uses {m.effectivePersist}</span>}
            </div>
            <SegmentedControl<PersistMode>
              size="sm"
              labelledBy={`persist-${m.id}`}
              value={m.persistMode}
              disabled={!!busy}
              onChange={(v) => void setPersist(v)}
              options={[
                { value: 'auto', label: 'Auto' },
                { value: 'tmux', label: 'tmux' },
                { value: 'direct', label: 'Direct' },
              ]}
            />
          </div>
        )}
      </div>

      <footer className="card-actions">
        {(canConnect || busy === 'Connect') && (
          <Button size="sm" variant={m.status === 'disconnected' || m.status === 'error' ? 'primary' : undefined} icon="plug" onClick={() => void act('Connect', () => api.connectMachine(m.id))} loading={busy === 'Connect'} disabled={!!busy && busy !== 'Connect'}>
            Connect
          </Button>
        )}
        <Button size="sm" icon="refresh" onClick={() => void act('Re-probe', () => api.probeMachine(m.id))} loading={busy === 'Re-probe'} disabled={!!busy && busy !== 'Re-probe'}>
          Re-probe
        </Button>
        <span className="spacer" />
        <Menu className="machine-more" label={`More actions for ${m.name}`} triggerLabel="More" items={more} />
      </footer>

      {orphans && <OrphansDialog machine={m} onClose={() => setOrphans(false)} />}
      {connLog && <ConnectionLogDialog machine={m} onClose={() => setConnLog(false)} />}

      {confirmDelete && <DeleteMachinesDialog machines={[m]} onClose={() => setConfirmDelete(false)} />}
    </article>
  );
}
