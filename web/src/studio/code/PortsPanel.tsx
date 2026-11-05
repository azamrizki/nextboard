// The Ports tab of the bottom panel: what listens on the project's machine, and a way to open it
// in a web tile (docs/API-studio.md §7.13).

import { useEffect, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { toast } from '../../components/Toast';
import { portsApi } from '../api';
import type { ListeningPort } from '../types';
import type { Workspace } from './workspace';

const EVERY_MS = 5000;

export function PortsPanel({ ws }: { ws: Workspace }) {
  const [items, setItems] = useState<ListeningPort[] | null>(null);
  const [all, setAll] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let stop = false;
    const load = () =>
      portsApi
        .listening(ws.mid)
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
  }, [ws]);

  // what the system names is the user's own; on a shared machine the rest is noise
  const mine = (items ?? []).filter((p) => p.process);
  const shown = all ? (items ?? []) : mine;
  return (
    <div className="wb-ports nowheel">
      <div className="wb-ports-head">
        <span className="text-3">{items === null ? 'Looking…' : `${shown.length} listening`}</span>
        {error && <span className="wb-task-bad">{error}</span>}
        <span className="spacer" />
        <label className="wb-check" style={{ marginTop: 0 }}>
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
          <span>Also other users’ ports</span>
        </label>
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
                  <Button size="sm" variant="ghost" icon="globe" onClick={() => ws.actions.openWeb?.(`http://localhost:${p.port}/`)}>
                    Open in a tile
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
    </div>
  );
}
