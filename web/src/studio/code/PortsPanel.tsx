// The Ports tab of the bottom panel: what listens on the machines of the card's parts, and a way
// to open it in a web tile on that machine (docs/API-studio.md §7.13), or in the Code view, which
// has no board, in a Browser tab. One section per machine ("server-b: admin, api") when the parts
// are on more than one.

import { useContext, useEffect, useState, type ReactNode } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { toast } from '../../components/Toast';
import { useMachines } from '../../store';
import { portsApi } from '../api';
import type { ListeningPort } from '../types';
import { PaneEnv } from './AgentPanes';
import { machinesOf } from './partGroups';
import { LOCAL_MACHINE } from './paths';
import type { Workspace } from './workspace';

const EVERY_MS = 5000;

export function PortsPanel({ ws }: { ws: Workspace }) {
  const [all, setAll] = useState(false);
  const machines = useMachines();
  const list = machinesOf(ws.paths.mounts);
  const name = (id: string) => (id === LOCAL_MACHINE ? 'This PC' : (machines.find((m) => m.id === id)?.name ?? id));
  const toggle = (
    <label className="wb-check" style={{ marginTop: 0 }}>
      <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
      <span>Also other users’ ports</span>
    </label>
  );
  // one machine: as it always was; several: the switch on top, a section per machine
  if (list.length === 1) {
    return (
      <div className="wb-ports nowheel">
        <MachinePorts ws={ws} machineId={list[0].machineId} title="" all={all} toggle={toggle} />
      </div>
    );
  }
  return (
    <div className="wb-ports nowheel">
      <div className="wb-ports-head">
        <span className="spacer" />
        {toggle}
      </div>
      {list.map((m) => (
        <MachinePorts key={m.machineId} ws={ws} machineId={m.machineId} title={`${name(m.machineId)}: ${m.parts.map((p) => p.name).join(', ')}`} all={all} />
      ))}
    </div>
  );
}

/** What listens on one machine; "Open in a tile" opens the page from that machine (audit #31). */
function MachinePorts({ ws, machineId, title, all, toggle }: { ws: Workspace; machineId: string; title: string; all: boolean; toggle?: ReactNode }) {
  const [items, setItems] = useState<ListeningPort[] | null>(null);
  const [error, setError] = useState('');
  // the Code view's editor is no card ("" node, CodeViewPage.tsx): its pages open as tabs
  const inTab = useContext(PaneEnv)?.nodeId === '';

  useEffect(() => {
    let stop = false;
    const load = () =>
      !document.hidden &&
      portsApi
        .listening(machineId)
        .then((list) => {
          if (stop) return;
          setItems(list);
          setError('');
        })
        .catch((err) => !stop && setError(errorMessage(err)));
    void load();
    const t = window.setInterval(load, EVERY_MS);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [machineId]);

  // what the system names is the user's own; on a shared machine the rest is noise
  const mine = (items ?? []).filter((p) => p.process);
  const shown = all ? (items ?? []) : mine;
  return (
    <section className="wb-ports-machine" aria-label={title || 'Ports'}>
      <div className="wb-ports-head">
        {title && <strong className="truncate">{title}</strong>}
        <span className="text-3">{items === null ? 'Looking…' : `${shown.length} listening`}</span>
        {error && <span className="wb-task-bad">{error}</span>}
        {toggle && <span className="spacer" />}
        {toggle}
      </div>
      {items && !shown.length && <p className="wb-panel-msg">Nothing of yours listens on this machine.</p>}
      {shown.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Port</th>
              <th>Program</th>
              <th>Address</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {shown.map((p) => (
              <tr key={p.port}>
                <td className="tabular mono">{p.port}</td>
                <td>{p.process ? `${p.process}${p.pid ? ` (${p.pid})` : ''}` : <span className="text-3">unknown</span>}</td>
                <td className="mono text-3">{p.address}</td>
                <td className="wb-ports-actions">
                  <Button size="sm" variant="ghost" icon="globe" onClick={() => ws.actions.openWeb?.(`http://localhost:${p.port}/`, machineId)}>
                    {inTab ? 'Open in a tab' : 'Open in a tile'}
                  </Button>
                  <Button size="sm" variant="ghost" icon="copy" onClick={() => void navigator.clipboard?.writeText(`localhost:${p.port}`).then(() => toast.success('Address copied.'))}>
                    Copy
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
