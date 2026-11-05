// Machines: This PC + agentless SSH machines, as a bento grid.

import { useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import type { Machine } from '../lib/types';
import { AddMachineDialog } from '../machines/AddMachineDialog';
import { HostKeyDialog, HostKeyMismatchDialog } from '../machines/HostKeyDialogs';
import { MachineAuthDialog } from '../machines/MachineAuthDialog';
import { MachineCard } from '../machines/MachineCard';
import { store, useMachines } from '../store';

export function MachinesPage() {
  const machines = useMachines();
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

  return (
    <div className="page">
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

        {loadError && (
          <div className="alert mb-4" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load machines: {loadError}</span>
          </div>
        )}

        <div className="bento machine-grid">
          {listed.map((m) => (
            <MachineCard key={m.id} machine={m} onVerifyHostKey={(mm) => setHostKeyFor(mm.id)} onEnterAuth={(mm) => setAuthFor(mm.id)} />
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
      </main>

      {adding && <AddMachineDialog onClose={() => setAdding(false)} onAdded={onAdded} />}

      {hostKeyMachine &&
        (hostKeyMachine.hostKeyMismatch ? (
          <HostKeyMismatchDialog machine={hostKeyMachine} onClose={() => setHostKeyFor(null)} />
        ) : (
          <HostKeyDialog machine={hostKeyMachine} onClose={() => setHostKeyFor(null)} onNeedsAuth={(m) => setAuthFor(m.id)} />
        ))}

      {authMachine && <MachineAuthDialog machine={authMachine} onClose={() => setAuthFor(null)} />}
    </div>
  );
}
