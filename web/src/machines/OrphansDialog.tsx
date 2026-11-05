// Lists tmux sessions (nb-*) on a machine that no longer have a node, and kills selected ones.

import { useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import type { Machine } from '../lib/types';

export function OrphansDialog({ machine, onClose }: { machine: Machine; onClose: () => void }) {
  const [names, setNames] = useState<string[] | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [killed, setKilled] = useState<string[] | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .orphans(machine.id)
      .then((list) => {
        setNames(list);
        setSelected(new Set(list));
      })
      .catch((err) => setError(errorMessage(err)));
  }, [machine.id]);

  const toggle = (n: string, on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(n);
      else next.delete(n);
      return next;
    });

  const cleanup = async () => {
    setBusy(true);
    setError('');
    try {
      const done = await api.cleanup(machine.id, [...selected]);
      setKilled(done);
      setNames((prev) => (prev ?? []).filter((n) => !done.includes(n)));
      setSelected(new Set());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  return (
    <Modal
      title={`Orphaned sessions on ${machine.name}`}
      icon="broom"
      onClose={() => !busy && onClose()}
      size="sm"
      help={confirming ? <span className="text-danger">Their processes will be terminated.</span> : undefined}
      footer={
        confirming ? (
          <>
            <Button variant="secondary" onClick={() => setConfirming(false)} disabled={busy}>
              Back
            </Button>
            <Button variant="danger" icon="trash" onClick={cleanup} loading={busy}>
              Kill {selected.size} session{selected.size === 1 ? '' : 's'}
            </Button>
          </>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose}>
              Close
            </Button>
            <Button variant="danger" disabled={!names || selected.size === 0} onClick={() => setConfirming(true)}>
              Kill selected…
            </Button>
          </>
        )
      }
    >
      <div className="stack">
        <p className="text-2">
          tmux sessions named <code>nb-*</code> that still run on the machine but have no terminal on the board.
        </p>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
        {killed && killed.length > 0 && (
          <div className="alert success" role="status">
            <Icon name="check" />
            <span>Killed {killed.join(', ')}.</span>
          </div>
        )}
        {!names && !error && (
          <p className="status">
            <span className="spinner" aria-hidden="true" /> Looking for orphaned sessions…
          </p>
        )}
        {names && names.length === 0 && <p className="text-2">No orphaned sessions — nothing to clean up.</p>}
        {names && names.length > 0 && (
          <div className="panel check-list" role="group" aria-label="Orphaned sessions">
            {names.map((n) => (
              <Checkbox key={n} checked={selected.has(n)} onChange={(on) => toggle(n, on)} disabled={busy} label={<code>{n}</code>} />
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}
