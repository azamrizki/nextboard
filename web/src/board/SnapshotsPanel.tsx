// Hourly snapshots of this workspace (newest first) with restore (asks first; the hub
// saves the current version as a snapshot before restoring).

import { useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { SnapshotInfo } from '../lib/types';
import type { BoardController } from './controller';

export function SnapshotsPanel({ ctl, onClose }: { ctl: BoardController; onClose: () => void }) {
  const [list, setList] = useState<SnapshotInfo[] | null>(null);
  const [error, setError] = useState('');
  const [confirm, setConfirm] = useState<SnapshotInfo | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let alive = true;
    setError('');
    api
      .snapshots(ctl.workspaceId)
      .then((l) => alive && setList(l))
      .catch((err) => alive && setError(errorMessage(err)));
    return () => {
      alive = false;
    };
  }, [ctl, reload]);

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  const restore = async (s: SnapshotInfo) => {
    await ctl.sync.flush();
    const board = await api.restoreSnapshot(ctl.workspaceId, s.id);
    ctl.sync.reset(board);
    ctl.clearHistory();
    ctl.select([]);
    toast.success(`Board restored to ${formatDateTime(s.at)}.`);
    setReload((n) => n + 1);
  };

  return (
    <aside className="side-panel" aria-label="Snapshots">
      <div className="side-panel-head">
        <Icon name="history" />
        <h2>Snapshots</h2>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh snapshots" onClick={() => setReload((n) => n + 1)} />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close snapshots" onClick={onClose} />
      </div>
      <p className="side-panel-hint">The hub keeps one snapshot for every hour the board changed (the newest 24).</p>
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {!list && !error && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading…
        </div>
      )}
      {list && list.length === 0 && <div className="list-msg">No snapshots yet. The first one is taken within the hour after a change.</div>}
      {list && list.length > 0 && (
        <ul className="snap-list">
          {list.map((s) => (
            <li key={s.id} className="snap-item">
              <div className="grow">
                <div className="snap-time">{formatDateTime(s.at)}</div>
                <div className="snap-meta">
                  {timeAgo(s.at)} · {s.nodeCount === 1 ? '1 tile' : `${s.nodeCount} tiles`}
                </div>
              </div>
              <Button size="sm" icon="restart" onClick={() => setConfirm(s)}>
                Restore
              </Button>
            </li>
          ))}
        </ul>
      )}
      {confirm && (
        <ConfirmDialog
          title="Restore this snapshot?"
          confirmLabel="Restore snapshot"
          icon="history"
          onClose={() => setConfirm(null)}
          onConfirm={() => restore(confirm)}
        >
          <p>
            The board goes back to <strong>{formatDateTime(confirm.at)}</strong>. The current version is saved as a snapshot first,
            so you can switch back. Running terminals keep running; tiles of terminals that have ended are dropped.
          </p>
        </ConfirmDialog>
      )}
    </aside>
  );
}
