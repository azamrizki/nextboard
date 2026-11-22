// Machines: This PC + agentless SSH machines, as a bento grid, and the archived ones. Select mode
// (DESIGN.md §3.18) archives, restores or deletes several; one in use says by what and stays.

import { useEffect, useMemo, useState } from 'react';
import { api, errorMessage } from '../api';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { HostSection } from '../host/HostSection';
import { SelectBar, SelectToggle, useSelectMode } from '../components/SelectBar';
import { toast } from '../components/Toast';
import type { Machine } from '../lib/types';
import { AddMachineDialog } from '../machines/AddMachineDialog';
import { ArchivedMachines, DeleteMachinesDialog, machinesAction, reportMachines } from '../machines/ArchivedMachines';
import { machineBlock, machineUsers } from '../machines/inUse';
import { HostKeyDialog, HostKeyMismatchDialog } from '../machines/HostKeyDialogs';
import { MachineAuthDialog } from '../machines/MachineAuthDialog';
import { MachineCard } from '../machines/MachineCard';
import { store, useArchivedMachines, useArchivedProjects, useMachines, useProjects } from '../store';

type MachinesTab = 'active' | 'archived';

export function MachinesPage() {
  const machines = useMachines();
  const archived = useArchivedMachines();
  const projects = useProjects();
  const archivedProjects = useArchivedProjects();
  const [tab, setTab] = useState<MachinesTab>('active');
  const [deleting, setDeleting] = useState<string[] | null>(null);
  const [working, setWorking] = useState('');
  const [adding, setAdding] = useState(false);
  const [hostKeyFor, setHostKeyFor] = useState<string | null>(null);
  const [authFor, setAuthFor] = useState<string | null>(null);
  const [loadError, setLoadError] = useState('');

  // Fresh list on open (the events stream keeps it live afterwards).
  useEffect(() => {
    api
      .machines()
      .then((list) => store.seedMachines(list))
      .catch((err) => setLoadError(errorMessage(err)));
    api
      .archivedMachines()
      .then((list) => store.seedArchivedMachines(list))
      .catch(() => undefined);
    // a part of an archived project keeps its machine in use too
    api
      .archivedProjects()
      .then((list) => store.seedArchivedProjects(list))
      .catch(() => undefined);
  }, []);

  const onAdded = (m: Machine) => {
    store.upsertMachine(m);
    setAdding(false);
    if (m.hostKeyMismatch || (m.status === 'hostkey' && m.pendingHostKey)) setHostKeyFor(m.id);
    else if (m.needsAuth) setAuthFor(m.id);
    else if (m.status === 'error') {
      const reason = (m.error || 'could not connect').replace(/\.?$/, '.');
      toast.error(`${m.name}: ${reason} Check the details, then press Connect.`);
    }
    else if (m.status === 'connected') toast.success(`Connected to ${m.name}.`);
  };

  // Always render the live version of the machine in the host key dialog.
  const hostKeyMachine = machines.find((m) => m.id === hostKeyFor);
  const authMachine = machines.find((m) => m.id === authFor);
  const remoteCount = machines.filter((m) => m.kind === 'ssh').length;
  // This PC first, the rest by name: a card stays where it is when its connection changes.
  const listed = [...machines].sort((a, b) => Number(b.id === 'local') - Number(a.id === 'local') || a.name.localeCompare(b.name));
  const online = machines.filter((m) => m.status === 'local' || m.status === 'connected').length;
  const terminals = machines.reduce((sum, m) => sum + m.sessionCount, 0);

  // why each one cannot be archived or deleted: This PC, a project's part, an open console
  const blocked = useMemo(() => {
    const all = [...projects, ...archivedProjects];
    return new Map(machines.map((m) => [m.id, machineBlock(m, machineUsers(m.id, all))]));
  }, [machines, projects, archivedProjects]);
  const ids = (tab === 'active' ? listed : archived).map((m) => m.id);
  const selectable = (id: string) => tab === 'archived' || !blocked.get(id);
  // one that comes into use while picked leaves the selection
  const select = useSelectMode(ids, ids.filter(selectable), selectable);
  const byId = (id: string) => machines.find((m) => m.id === id) ?? archived.find((m) => m.id === id);
  const picked = select.picked.map(byId).filter((m): m is Machine => !!m);
  const changeTab = (t: MachinesTab) => {
    select.stop();
    setTab(t);
  };
  useEffect(() => {
    if (tab === 'archived' && archived.length === 0 && !select.on) setTab('active');
  }, [tab, archived.length, select.on]);

  const bulk = async (action: 'archive' | 'restore') => {
    setWorking(action);
    try {
      const out = await machinesAction(picked, action);
      reportMachines(out, picked, action === 'archive' ? 'archived' : 'restored');
      if (!out.failed.length) select.stop();
    } catch (err) {
      toast.error(`Could not ${action}: ${errorMessage(err)}`);
    } finally {
      setWorking('');
    }
  };
  const selectableCount = tab === 'active' ? listed.filter((m) => !blocked.get(m.id)).length : archived.length;

  return (
    <div className={`page ${select.on ? 'has-select-bar' : ''}`}>
      <AppHeader />
      <main className="content">
        <div className="page-head">
          <div>
            <h1>Machines</h1>
            <p>Where your agents run. Remote machines are reached over SSH — nothing is installed on them.</p>
            {machines.length > 1 && (
              <div className="machines-summary">
                <span className="badge badge-success">
                  <span className="dot dot-success" aria-hidden="true" />
                  {online} online
                </span>
                {online < machines.length && (
                  <span className="badge badge-warning">
                    <span className="dot dot-warning" aria-hidden="true" />
                    {machines.length - online} not connected
                  </span>
                )}
                <span className="badge">
                  <Icon name="terminal" />
                  {terminals} terminal{terminals === 1 ? '' : 's'}
                </span>
              </div>
            )}
          </div>
          <Button variant="primary" icon="plus" onClick={() => setAdding(true)}>
            Add machine
          </Button>
        </div>

        {(remoteCount > 0 || archived.length > 0) && (
          <div className="home-views">
            {(archived.length > 0 || tab === 'archived') && (
              <SegmentedControl<MachinesTab>
                size="sm"
                label="Show"
                value={tab}
                onChange={changeTab}
                options={[
                  { value: 'active', label: <>Machines <span className="seg-count tabular">{machines.length}</span></> },
                  { value: 'archived', label: <>Archived <span className="seg-count tabular">{archived.length}</span></>, title: 'Not connected and out of the pickers; Restore brings one back' },
                ]}
              />
            )}
            <span className="spacer" />
            <SelectToggle mode={select} label="machines" disabled={selectableCount === 0 && !select.on} />
          </div>
        )}

        {loadError && (
          <div className="alert mb-4" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load machines: {loadError}</span>
          </div>
        )}

        {tab === 'archived' && <ArchivedMachines machines={archived} select={select} onDelete={(list) => setDeleting(list.map((m) => m.id))} />}

        {tab === 'active' && !select.on && <HostSection />}

        {tab === 'active' && (
          <div className="bento machine-grid">
            {listed.map((m) => (
              <MachineCard key={m.id} machine={m} onVerifyHostKey={(mm) => setHostKeyFor(mm.id)} onEnterAuth={(mm) => setAuthFor(mm.id)} select={select} blocked={blocked.get(m.id) ?? ''} />
            ))}
            {remoteCount === 0 && machines.length > 0 && (
              <button type="button" className="card card-clickable pair-card" onClick={() => setAdding(true)}>
                <div className="card-head">
                  <span className="card-icon" aria-hidden="true">
                    <Icon name="plus" />
                  </span>
                  <div className="card-title">
                    <h3>No remote machines yet</h3>
                    <span className="card-meta">Add one from your ~/.ssh/config or by hand.</span>
                  </div>
                </div>
              </button>
            )}
          </div>
        )}
      </main>

      {adding && <AddMachineDialog onClose={() => setAdding(false)} onAdded={onAdded} />}

      {hostKeyMachine &&
        (hostKeyMachine.hostKeyMismatch ? (
          <HostKeyMismatchDialog machine={hostKeyMachine} onClose={() => setHostKeyFor(null)} />
        ) : (
          <HostKeyDialog machine={hostKeyMachine} onClose={() => setHostKeyFor(null)} onNeedsAuth={(m) => setAuthFor(m.id)} />
        ))}

      {authMachine && <MachineAuthDialog machine={authMachine} onClose={() => setAuthFor(null)} />}

      {deleting && <DeleteMachinesDialog machines={deleting.map(byId).filter((m): m is Machine => !!m)} onClose={() => setDeleting(null)} onDone={() => select.stop()} />}

      <SelectBar mode={select} label="machines">
        {tab === 'active' ? (
          <Button size="sm" icon="archive" disabled={!select.count} loading={working === 'archive'} onClick={() => void bulk('archive')}>
            Archive
          </Button>
        ) : (
          <Button size="sm" icon="restart" disabled={!select.count} loading={working === 'restore'} onClick={() => void bulk('restore')}>
            Restore
          </Button>
        )}
        <Button size="sm" variant="danger-ghost" icon="trash" disabled={!select.count || !!working} onClick={() => setDeleting(select.picked)}>
          Delete permanently
        </Button>
      </SelectBar>
    </div>
  );
}
