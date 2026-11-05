// Archived boards (closed workspaces, removed projects, cleared / unreadable boards).
// Restoring one adds it as a new workspace to a project (without terminal tiles).

import { useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import { boardPath, navigate } from '../lib/router';
import type { ArchiveEntry } from '../lib/types';
import { store, useProjects } from '../store';

const REASON: Record<string, string> = { closed: 'Closed', cleared: 'Cleared', unreadable: 'Unreadable' };

export function ArchiveDialog({ onClose }: { onClose: () => void }) {
  const projects = useProjects();
  const [list, setList] = useState<ArchiveEntry[] | null>(null);
  const [error, setError] = useState('');
  const [target, setTarget] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState('');

  useEffect(() => {
    api
      .archive()
      .then((l) => setList(l.sort((a, b) => b.archivedAt.localeCompare(a.archivedAt))))
      .catch((err) => setError(errorMessage(err)));
  }, []);

  const defaultTarget = (e: ArchiveEntry) => projects.find((p) => p.name === e.projectName)?.id ?? projects[0]?.id ?? '';

  const restore = async (e: ArchiveEntry) => {
    const pid = target[e.id] ?? defaultTarget(e);
    if (!pid) return;
    setBusy(e.id);
    try {
      const ws = await api.restoreArchive(e.id, pid);
      const p = store.getState().projects[pid];
      if (p) store.upsertProject({ ...p, workspaces: [...(p.workspaces ?? []).filter((w) => w.id !== ws.id), ws] });
      toast.success(`Restored “${e.workspaceName}” as workspace “${ws.name}”.`);
      onClose();
      navigate(boardPath(pid, ws.id));
    } catch (err) {
      toast.error(`Could not restore: ${errorMessage(err)}`);
      setBusy('');
    }
  };

  return (
    <Modal
      title="Archive"
      icon="archive"
      size="lg"
      onClose={onClose}
      help="Archived boards are kept until you delete the hub’s data folder."
      footer={
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
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
      {list && list.length === 0 && <div className="list-msg">Nothing archived. Closed workspaces and removed projects land here.</div>}
      {list && list.length > 0 && (
        <ul className="archive-list">
          {list.map((e) => (
            <li key={e.id} className="archive-item">
              <div className="grow">
                <div className="archive-title truncate">
                  {e.workspaceName} <span className="text-3">· {e.projectName}</span>
                </div>
                <div className="archive-meta">
                  {formatDateTime(e.archivedAt)} · {e.nodeCount === 1 ? '1 tile' : `${e.nodeCount} tiles`} ·{' '}
                  <span className="badge">{REASON[e.reason] ?? e.reason}</span>
                </div>
              </div>
              <select
                className="control control-sm archive-target"
                aria-label={`Project to restore “${e.workspaceName}” into`}
                value={target[e.id] ?? defaultTarget(e)}
                onChange={(ev) => setTarget((t) => ({ ...t, [e.id]: ev.target.value }))}
                disabled={projects.length === 0}
              >
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
              <Button size="sm" icon="restart" loading={busy === e.id} disabled={projects.length === 0 || (!!busy && busy !== e.id)} onClick={() => void restore(e)}>
                Restore
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}
