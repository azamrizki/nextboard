// An agent asks to write to a database (docs/API-db.md, Agents): a pill while a request waits, and
// the dialog that shows its SQL, why, and how many rows it touches. A dangerous one (DROP,
// TRUNCATE, no WHERE) needs the connection's name typed, as the owner's own would.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { useProject } from '../store';
import { dbApi } from './api';
import { DriverMark, EnvTag } from './Confirm';
import { useDbRequests } from './store';
import type { DbRequest } from './types';

const openers = new Set<(id?: string) => void>();
/** Opens the oldest request waiting (an attention's click), or the one named. */
export const openDbRequest = (id?: string) => openers.forEach((fn) => fn(id));

const ENDED: Record<string, string> = {
  running: 'You approved it. It is running.',
  done: 'It ran.',
  denied: 'It was denied.',
  expired: 'Nobody approved it in time.',
  failed: 'It could not run.',
};

function left(r: DbRequest): string {
  const s = Math.max(0, Math.round((Date.parse(r.expiresAt) - Date.now()) / 1000));
  return s >= 60 ? `${Math.round(s / 60)} min` : `${s} s`;
}

function ApproveDialog({ r, onClose }: { r: DbRequest; onClose: () => void }) {
  const project = useProject(r.projectId);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);
  const waiting = r.state === 'waiting';

  const approve = async (session: boolean) => {
    setBusy(true);
    try {
      await dbApi.approve(r.id, { session, confirmName: r.danger ? typed : undefined });
      toast.success(session ? `${r.who} may write to ${r.connection} until its session ends` : 'Approved');
      onClose();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const deny = async () => {
    setBusy(true);
    try {
      await dbApi.deny(r.id);
      onClose();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`${r.who} asks to ${r.redis ? 'run a command on' : 'write to'} ${r.connection}`}
      icon="database"
      tone={r.danger ? 'danger' : undefined}
      size="md"
      onClose={onClose}
      footer={
        waiting ? (
          <>
            <span className="text-3 fs-xs db-foot-note">Waits {left(r)} more</span>
            <Button variant="ghost" loading={busy} onClick={() => void deny()}>
              Deny
            </Button>
            {!r.danger && (
              <Button loading={busy} onClick={() => void approve(true)} title="Its next writes to this connection run without asking, until the agent's session ends">
                Allow for this session
              </Button>
            )}
            <Button variant={r.danger ? 'danger' : 'primary'} loading={busy} disabled={!!r.danger && typed !== r.connection} onClick={() => void approve(false)}>
              {r.danger ? 'Run it' : 'Approve once'}
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        )
      }
    >
      <div className="db-conn-line">
        <DriverMark driver={r.driver} />
        <b>{r.connection}</b>
        <span className="text-3">· {project?.name ?? 'a project'}</span>
        <EnvTag env={r.env} />
      </div>
      {r.why && (
        <p className="db-why">
          <Icon name="message" /> {r.why}
        </p>
      )}
      <pre className="db-sql-preview">{r.sql}</pre>
      {r.rows >= 0 && !r.redis && (
        <p className="text-2 fs-sm">
          Touches about <b>{r.rows.toLocaleString()}</b> row{r.rows === 1 ? '' : 's'}.
        </p>
      )}
      {r.danger && (
        <>
          <div className="db-banner db-banner-danger">{r.danger}. It runs in a transaction the agent commits or rolls back after seeing what it touched; MySQL commits DROP and TRUNCATE at once.</div>
          {waiting && (
            <label className="db-field">
              <span className="db-label">
                Type <b className="mono">{r.connection}</b> to let it run
              </span>
              <input className="control db-input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={`Type ${r.connection} to let it run`} />
            </label>
          )}
        </>
      )}
      {!waiting && (
        <div className={`db-banner ${r.isError ? 'db-banner-danger' : 'db-banner-info'}`}>
          {ENDED[r.state] ?? r.state}
          {r.result ? <pre className="db-result-text">{r.result}</pre> : null}
        </div>
      )}
    </Modal>
  );
}

export function DbRequests() {
  const items = useDbRequests();
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    const fn = (id?: string) => setOpen(id ?? '');
    openers.add(fn);
    return () => {
      openers.delete(fn);
    };
  }, []);
  const waiting = items.filter((r) => r.state === 'waiting');
  const current = open === null ? undefined : open === '' ? waiting[0] : items.find((r) => r.id === open);
  return (
    <>
      {waiting.length > 0 && !current && (
        <button type="button" className="vault-request-pill db-request-pill" onClick={() => setOpen(waiting[0].id)}>
          <Icon name="database" />
          <span>
            {waiting[0].who} asks to write to {waiting[0].connection}
            {waiting.length > 1 ? ` (+${waiting.length - 1})` : ''}
          </span>
        </button>
      )}
      {current && <ApproveDialog key={current.id} r={current} onClose={() => setOpen(null)} />}
    </>
  );
}
